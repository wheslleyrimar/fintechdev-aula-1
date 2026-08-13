# Roteiro de demonstração — Aula 1

Conduzido pelo **painel** (http://localhost:8080). Nenhum terminal na frente da turma.
Tempo total: **~35 minutos**.

Antes de começar:

```bash
make reset && make up && make painel
```

`reset` apaga o volume do Postgres — Ana volta com R$ 10.000 e o log fica limpo.
Deixe uma segunda janela com `make logs` se quiser mostrar o servidor reagindo.

---

## 0 · Visão geral (3 min) — §2, §3

Abra o painel. Sem clicar em nada, três coisas já estão na tela:

1. **Ativo = Passivo**, com o selo `CONSERVADO`. Pergunte por que o saldo da Ana aparece como
   **passivo**. Resposta: aquele dinheiro não é nosso — nós devemos a ela.
2. **Invariantes agora**: cinco checagens rodando contra o banco de verdade.
3. **Borda eventual**: zero lançamentos não projetados, com atraso proposital de 250ms.

Aponte o rodapé da barra lateral: `lock optimistic · pool 20 · projeção +250ms · SPI timeout 8000ms`.
São as alavancas de arquitetura, visíveis sem abrir código.

> "Um monólito modular. Um processo, um banco, seis módulos com fronteiras que o **compilador**
> garante. Nenhum número desta tela é uma coluna de saldo — todos são soma sobre o log."

---

## 1 · Os três toques da Ana (8 min) — §1, §4

Aba **Os três toques**. Deixe o valor em R$ 5.000 (o da narrativa) e clique
**"Ana toca 3× ao mesmo tempo"**.

> ⚠ **Aula depois das 20h?** O limite noturno (20h–06h, horário de Brasília) é de R$ 1.000 e
> os três toques vão voltar `RECUSADO · LIMITE_NOTURNO`. Duas saídas: use R$ 500 no lugar de
> R$ 5.000, ou suba `PIX_NIGHT_LIMIT_CENTS` no `docker-compose.yml` e rode
> `docker compose up -d techpix`.
>
> Se acontecer sem querer, **aproveite**: é a §6.5 passo 3 (falhar fechado) se apresentando
> sozinha, e o painel já explica que essa recusa acontece *antes* do ledger — nada foi gravado,
> nem a chave de idempotência foi usada.

O que apontar, na ordem em que aparece:

1. **O EndToEndId** aparece ao lado dos botões. Formato do BACEN: `E` + ISPB + timestamp +
   aleatório = 32 caracteres. Pergunte: *"e se essa chave nascesse no servidor?"*
   Resposta: cada retry teria chave nova e a deduplicação seria uma ilusão.
2. **Os três cartões**: um `201 EXECUTOU`, dois `200 REPLAY`. Repare no **tempo**: o vencedor
   demora ~450ms (foi até o SPI), os replays voltam em ~200ms — eles esperaram só a *reserva*
   commitar. Isso é o estado `IN_PROGRESS` fazendo o trabalho.
3. **O mesmo `tx reserva`** nos três cartões.
4. **Efeito no dinheiro**: saiu da conta exatamente o valor de **um** pagamento.
5. **O que foi para o log**: duas transações, quatro lançamentos, cada par com `Σ = Σ ✓`.

> "Ana tocou 3×. Aconteceu 1×. Foi respondida 3×. Isso não se resolveu no código da tela —
> se resolveu na arquitetura."

**Variação que vale ouro:** desmarque *"mesmo EndToEndId nos três toques"* e dispare de novo.
Agora são três intenções diferentes e o sistema debita três vezes — **e está certo**.
Idempotência não é "bloquear repetição", é "honrar cada intenção uma vez".

---

## 2 · O timeout é ambíguo (8 min) — §4.1, §4.5

Aba **SPI e reconciliação**. Antes de clicar, faça a pergunta e **espere** a turma responder:

> "O SPI vai liquidar de verdade — final e irrevogável. A resposta vai sumir no caminho.
> Do nosso lado, isso é só um timeout. O que a gente faz?"

Aparecem sempre as duas respostas erradas: **estornar** (destrói dinheiro já liquidado) e
**reenviar** (duplicaria, se o E2E ID não fosse único).

Agora marque **buraco negro**, clique **Aplicar no BACEN**, e depois **Pagar e acompanhar**.

1. Oito segundos de espera — mostre o cronômetro subindo. É o `SPI_TIMEOUT_MS`.
2. `HTTP 202` e o pagamento fica em **RESERVED**. A linha do tempo explica por que não estornamos.
3. A linha **"⚠ O SPI JÁ tinha liquidado"** aparece: o painel pergunta ao simulador pelo E2E ID.
   A verdade sempre existiu lá; só não tinha chegado até nós.
4. A reconciliação resolve sozinha em segundos e o status vira **SETTLED**.
5. Por último, a caixa amarela: **a janela de incerteza medida em segundos**.

> "Durante N segundos o dinheiro já era do recebedor pelas leis do SPB, e o TechPix não sabia.
> Não estava errado: estava **incerto**, e sabia disso. O tamanho dessa janela é decisão de
> negócio — `SPI_TIMEOUT_MS` + `RECONCILER_AFTER_S` — não acidente técnico."

Desmarque o buraco negro e aplique de novo antes de seguir.

**Se sobrar tempo:** arraste *taxa de rejeição* para 100% e pague. O SPI responde `RJCT`,
o ledger grava um **estorno** — uma transação NOVA — e o `pix_liquidacao` nunca acontece,
porque o dinheiro nunca saiu do SPB.

---

## 3 · Forte no núcleo, eventual na borda (5 min) — §5.5

Aba **Forte vs eventual** → **Disparar Pix e observar a divergência**.

O gráfico mostra a linha azul (ledger) caindo na hora e a roxa (projeção) caindo ~350ms depois.
A faixa amarela é a janela em que as duas discordam. O rótulo anuncia quando a borda alcançou o log.

> "Ver o extrato com 250ms de atraso não machuca ninguém. Debitar errado, sim. Por isso quem
> autoriza pagamento é o ledger — **nunca** a projeção."

Feche com a assimetria: apagar a projeção é recuperável; apagar `entries` acaba com o sistema.

---

## 4 · O DICT é caro (5 min) — §6.4

Aba **DICT e token bucket**. Clique **Reiniciar balde** para começar em 100.

Na ordem:

1. **Chave válida** → 1 token. Clique **de novo**: o log mostra `0 · cache`. Não tocou no BACEN.
2. **CPF inválido** → `0 · bloqueio local`. O dígito verificador não fecha; a chave nunca
   existiria no DICT. *"Consultar teria queimado 20 tokens à toa."*
3. **Chave inexistente** → 20 tokens de uma vez. Mostre o balde despencando.
4. **Varredura 5×** → o balde zera. Cinco consultas. **Cinco.**
5. Continue clicando: `429` do BACEN e, na sequência, o **semáforo do breaker fica vermelho** —
   o TechPix passa a responder `503` sem sequer tentar.

> "O DICT está no caminho crítico e é síncrono: ele acopla a nossa disponibilidade à de um
> sistema que não é nosso. Timeout curto, cache e circuit breaker decidem se um soluço no DICT
> derruba pagamentos de clientes que nada têm a ver com isso."

---

## 5 · Onde nasce o ponto quente (5 min) — §3.7

Aba **Contenção**. A conta já vem na Carla. Clique **Preparar saldo (6× o valor)** e depois
**Disparar simultâneos**.

Doze pagamentos ao mesmo tempo, cada um com E2E ID próprio — **sem idempotência para salvar**,
porque são intenções diferentes.

- A grade acende: 6 verdes, 6 vermelhos (`SALDO_INSUFICIENTE`).
- **Saldo final: R$ 0,00.** Não estourou nem sobrou.
- **Retries de serialização** com um número maior que zero: é a prova de que houve disputa real.
  O SSI abortou e refez transações para que ninguém lesse saldo obsoleto.

> "2.700 escritas por segundo são troco para um NVMe de 500 mil IOPS. O gargalo nunca foi
> capacidade — é **coordenação**."

Se houver tempo: troque `LEDGER_LOCK_MODE` para `pessimistic`, `docker compose up -d techpix`,
e repita. Otimista aborta e retenta; pessimista enfileira.

---

## 6 · O Harness (4 min) — §7.4

Aba **Harness**. As cinco invariantes verdes, com o detalhe de cada uma.

Agora os quatro botões vermelhos, um por vez. Cada um reporta **qual camada barrou**:

| Tentativa | Quem reage |
|---|---|
| Criar dinheiro do nada (D 1,00 / C 9,00) | aplicação, antes de tocar no banco |
| Crédito solto (INSERT direto) | banco — constraint trigger, cobrada no `COMMIT` |
| Alterar lançamento gravado (`UPDATE`) | banco — trigger `BEFORE UPDATE OR DELETE` |
| Estourar o saldo | aplicação, dentro da transação `SERIALIZABLE` |

A mensagem de erro do Postgres aparece na tela, literal.

> "O Harness precisa ser projetado **dentro** do sistema, não pregado por fora depois. É isso
> que torna aceitável deixar um agente propor mudanças aqui: nenhuma proposta escapa do
> guardrail — e a ferramenta para debitar uma conta simplesmente não existe."

Se quiser mostrar que também é teste automático, rode `make test` (16 testes) fora da aula
ou numa janela lateral.

---

## 7 · Fechamento (2 min) — §9

Aba **Orçamento de latência**. A barra mostra para onde foi cada milissegundo do último
pagamento, e a tabela traz p50/p99/p99.9 contra os números oficiais (teto 40s, SPI p99 4,6s,
DICT p99 1s).

> "Latência é distribuição, não média. Em fintech a cauda manda: é nela que o cliente desiste,
> o timeout dispara e o retry nasce."

Termine com a pergunta da aula:

> "Onde, no sistema de vocês, uma decisão está sendo tomada **na fé**, sem evidência? Anotem.
> É exatamente aí que arquitetura evolutiva e IA vão agir na Aula 8."

Aqui mesmo há três, listadas na seção "Revisão" do [ADR-001](ADR-001-consistencia-forte-no-ledger.md).

---

## Plano B

- O painel é servido pelo próprio binário: se o Docker sobe, ele existe. Sem npm, sem CDN,
  sem internet.
- Se o navegador falhar no projetor, os mesmos experimentos existem em terminal:
  `make demo-ana`, `make demo-reconcile`, `make demo-eventual`, `make demo-dict`,
  `make demo-contencao`.
- Se o Docker falhar: `docker compose up -d postgres bacen-sim` + `go run ./cmd/techpix`
  (com `DATABASE_URL` e `BACEN_BASE_URL` apontando para `localhost`).
- Se nada subir: o código comentado e os ADRs sustentam a aula sozinhos — cada arquivo cita
  a seção correspondente.
