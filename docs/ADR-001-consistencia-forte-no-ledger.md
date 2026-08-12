# ADR-001 · Consistência forte no ledger do core

**Status:** Aceito · **Data:** 2026-08-12 · **Formato:** Architecture Decision Record (Nygard, 2011)

## Contexto

O Pix é irreversível e liquida em moeda de banco central, no SPI. O ledger não pode
criar nem destruir dinheiro, não pode permitir saldo negativo, e cada movimento
precisa ser explicável anos depois (BACEN/LGPD).

Operamos sob teto normativo de 40 segundos ponta a ponta no canal primário do SPI
(Resolução BCB nº 195/2022). O SPI real entrega p50 ≈ 2,8s e p99 ≈ 4,6s; o DICT tem
SLA de p99 ≤ 1s para consulta de chaves.

## Decisão

A escrita no ledger é **ACID e fortemente consistente (linearizável)**, síncrona no
caminho crítico, e **idempotente pelo EndToEndId**.

Concretamente, neste repositório:

| Decisão | Onde vive no código |
|---|---|
| Toda escrita de dinheiro roda em `SERIALIZABLE` com retry | [pg.go](../internal/platform/pg/pg.go) → `InSerializableTx` |
| Saldo é derivado do log, nunca uma coluna | [001_ledger.sql](../migrations/001_ledger.sql) → `account_balance()` |
| Log é append-only, imposto pelo banco | trigger `entries_append_only` |
| Σ débitos = Σ créditos, imposto pelo banco | constraint trigger `entries_double_entry` (DEFERRED) |
| E2E ID único | índice `transactions_e2e_kind_uniq` |
| Efeito exactly-once | [idempotency](../internal/modules/idempotency/) — chave + estado + atomicidade |
| Chamada externa nunca dentro de transação aberta | [pix/module.go](../internal/modules/pix/module.go) → passo 5 fora da tx |

## Consequências

**(+)** Correção garantida: nem sob concorrência o saldo estoura. Provado em
`TestConcorrenciaNaoDeixaSaldoEstourar`.

**(+)** Trilha de auditoria completa: o estado atual é recalculável a partir do log.
Se a projeção for perdida, ela se reconstrói; se o log for perdido, acabou.

**(+)** As invariantes viraram teste — base do Harness (`/v1/fitness` e `make test`).

**(−)** Custo de latência na escrita, consumindo parte do teto de 40s. Medido por
requisição em `orcamento_latencia` e agregado em `/v1/latencia`.

**(−)** A escrita não escala horizontalmente como a leitura. Sob contenção alta o SSI
aborta e retenta (`ledger.serialization_retry`). É o "ponto quente" que a Aula 2 vai
medir.

## Alternativas rejeitadas

**Ledger eventualmente consistente.** REJEITADO: viola conservação e auditoria.
Duas escritas concorrentes se sobrepõem (lost update) e o resultado é dinheiro criado
ou destruído — o pior defeito possível numa fintech.

**Saldo numa coluna com `UPDATE`.** REJEITADO: sem auditoria (sabe o "agora", não o
"como chegou"), sujeito a corrida, e não reconstruível — se o número corromper, a
verdade se perdeu, porque a verdade *era* aquele número.

**Two-phase commit entre serviços.** Fora de escopo hoje: não há serviços. Ver ADR-002.

## Revisão

A consequência de latência será **medida em produção**. Se o p99 do passo
`4_ledger_reserva` ameaçar o orçamento, reavaliar em um novo ADR — na Aula 8, com um
agente propondo a mudança sob guardrails, via MCP (o agente **lê** produção e
**propõe**; nunca move dinheiro).

Sinais a observar, já instrumentados:

- `ledger.serialization_retry` e `ledger.serialization_exhausted`
- quantis de `4_ledger_reserva` em `/v1/latencia`
- `lancamentos_nao_projetados` (defasagem da borda)
