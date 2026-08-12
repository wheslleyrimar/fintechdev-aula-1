# ADR-002 · Monólito modular, com fronteiras verificadas pelo compilador

**Status:** Aceito · **Data:** 2026-08-12

## Contexto

A régua de evolução no fim da Aula 1 diz: **monólito TechPix (uma app, um deploy)**.

O ADR-001 exige que "marcar a intenção como concluída" e "gravar os lançamentos"
aconteçam na mesma transação. Isso é trivial dentro de um processo com um banco, e
caro entre processos: exigiria 2PC (atomicidade forte, custo em latência e
fragilidade) ou saga (atomicidade eventual com compensação explícita).

Nenhuma das duas se justifica hoje: não temos o problema de escala nem de autonomia
de times que motiva a separação.

## Decisão

Um único binário (`cmd/techpix`), um único deploy, um único banco, com **seis módulos
de fronteira real**:

```
internal/modules/
  ledger/       api.go  ← contrato público      internal/store/  ← invisível para os outros
  idempotency/  api.go                          internal/store/
  bacen/        api.go                          internal/{dict,spi,breaker}/
  pix/          api.go                          internal/{store,risco}/
  accounts/     module.go
  statement/    api.go                          internal/store/
```

A fronteira **não é convenção**: é a regra do `internal/` do Go. Um `import` de
`ledger/internal/store` a partir do módulo `pix` **não compila**. Ninguém escreve na
tabela `entries` pelas costas do ledger — não por disciplina, por compilador.

Dependências entre módulos são declaradas por interface e ligadas num único lugar
([cmd/techpix/main.go](../cmd/techpix/main.go)). Se essa função de composição ficar
difícil de ler, a arquitetura azedou — e a gente descobre cedo, numa tela.

O `pg.Tx` aparece na assinatura de `ledger.RegistrarTx` e de `idempotency.Efeito`.
É infraestrutura atravessando a fronteira, e é deliberado: é o preço explícito da
atomicidade exigida pelo ADR-001. O dia em que esse tipo precisar sumir é
exatamente o dia em que o módulo virou serviço.

## Consequências

**(+)** Uma transação, um commit: idempotência e ledger atômicos sem 2PC.

**(+)** Refatorar fronteira é barato enquanto o domínio ainda está se formando.

**(+)** Uma stack sobe em `docker compose up` — a turma inteira roda em 2 minutos.

**(−)** Todos os módulos escalam juntos: não dá para dar 10 réplicas só ao `pix`.

**(−)** Um deploy ruim derruba tudo. Mitigação: entrega progressiva e guardrails
(Harness) — Aula 8.

**(−)** A fronteira só se sustenta se ninguém fizer módulo importar módulo por
atalho. O `internal/` protege o *interior*; o contrato público continua sendo
responsabilidade de quem revisa.

## Alternativas rejeitadas

**Microsserviços desde o início.** REJEITADO: pagaria latência de rede, 2PC ou saga, e
complexidade operacional para resolver um problema de escala que ainda não existe.

**Monólito sem módulos.** REJEITADO: sem fronteira, o ledger vira "tabela que todo
mundo escreve" — e a invariante de partida dobrada morre no primeiro `UPDATE`
oportunista.

## Revisão

Reavaliar quando aparecer pelo menos um destes sinais:

- um módulo com perfil de escala radicalmente diferente dos outros;
- times distintos disputando o mesmo ciclo de deploy;
- necessidade de isolar falha (um módulo instável derrubando o resto).

O primeiro candidato natural a sair é `statement` (borda, eventual, leitura pura).
O último é `ledger` — e provavelmente ele nunca sai.
