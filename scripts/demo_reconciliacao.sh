#!/usr/bin/env bash
# =============================================================================
# Aula 1 · §4.1 e §4.5 — O timeout e AMBIGUO.
#
# Ligamos o modo "buraco negro" no SPI: a liquidacao ACONTECE (final e
# irrevogavel), mas a resposta nunca chega. Do nosso lado isso e apenas um
# timeout — indistinguivel de "nada aconteceu".
#
# O que NAO se pode fazer: estornar por conta propria (destruiria dinheiro ja
# liquidado) ou reenviar cegamente (duplicaria). O que se faz: reconciliar
# por E2E ID.
# =============================================================================
set -euo pipefail
cd "$(dirname "$0")"
source ./_lib.sh
exige_api

titulo "Quando a resposta do SPI se perde"

passo "1. Liga o buraco negro no SPI (100% das instrucoes) e encurta o timeout"
curl -fsS -X POST "$BACEN/admin/config" -H 'Content-Type: application/json' \
  -d '{"spi_taxa_buraco_negro":1.0}' | json "'buraco negro = %s' % d['spi_taxa_buraco_negro']"
nota "SPI_TIMEOUT_MS do TechPix esta em $(curl -fsS "$API/" | json "d['config_visivel']['spi_timeout_ms']")ms."
pausa

passo "2. Ana paga R$ 77,00. O SPI vai liquidar e engolir a resposta."
E2E=$(curl -fsS -X POST "$API/v1/pix/e2e" | json "d['e2e_id']")
nota "E2E ID = $E2E"
curl -sS -o /tmp/techpix_buraco.json -w "  HTTP %{http_code} em %{time_total}s (o timeout do SPI estourou)\n" \
  -X POST "$API/v1/pix/pagamentos" -H 'Content-Type: application/json' \
  -d "{\"e2e_id\":\"$E2E\",\"conta_pagador\":\"carteira:ana\",\"chave_recebedor\":\"bruno@bancobeta.com.br\",\"valor_centavos\":7700}"
bonito < /tmp/techpix_buraco.json
pausa

passo "3. Estado do pagamento: preso em RESERVED (limbo EXPLICITO)"
curl -fsS "$API/v1/pix/pagamentos/$E2E" | bonito
nota "O dinheiro saiu da carteira da Ana e esta em 'pix_a_liquidar'."
nota "Nao sumiu, nao duplicou: esta num pote com nome, esperando desfecho."

passo "4. E o SPI, o que ele sabe? (a verdade sempre existiu la)"
curl -fsS "$BACEN/spi/v1/payments/$E2E" | bonito
pausa

passo "5. A reconciliacao roda sozinha. Esperando..."
for i in $(seq 1 20); do
  STATUS=$(curl -fsS "$API/v1/pix/pagamentos/$E2E" | json "d['status']")
  printf "  t+%02ds  status=%s\n" "$i" "$STATUS"
  if [ "$STATUS" != "RESERVED" ]; then
    ok "reconciliado como $STATUS"
    break
  fi
  sleep 1
done

passo "6. Transacoes do ledger para este E2E ID"
curl -fsS "$API/v1/ledger/e2e/$E2E" | bonito
nota "Repare: a reserva NAO foi apagada. A liquidacao e uma NOVA transacao."
nota "Dinheiro e irreversivel — desfazer sempre e um novo fato, nunca um DELETE."

passo "7. Invariantes seguem de pe?"
curl -fsS "$API/v1/fitness" | bonito

passo "8. Desliga o buraco negro"
curl -fsS -X POST "$BACEN/admin/config" -H 'Content-Type: application/json' \
  -d '{"spi_taxa_buraco_negro":0.0}' | json "'buraco negro = %s' % d['spi_taxa_buraco_negro']"

titulo "O que a aula quer que fique"
nota "at-most-once  -> nao duplica, mas PERDE. Inaceitavel."
nota "at-least-once -> nao perde, mas DUPLICA. E o que a rede entrega."
nota "exactly-once  -> impossivel em rede assincrona."
ok  "efeito exactly-once -> possivel: idempotencia + reconciliacao por E2E ID."
