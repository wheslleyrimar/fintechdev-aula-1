#!/usr/bin/env bash
# =============================================================================
# Aula 1 · §5.5 — Forte no nucleo, eventual na borda.
#
# A linha que resolve 80% da arquitetura de dados de uma fintech:
#   ledger  -> consistencia FORTE (autoriza pagamento)
#   extrato -> consistencia EVENTUAL (100-300ms de atraso, e tudo bem)
# =============================================================================
set -euo pipefail
cd "$(dirname "$0")"
source ./_lib.sh
exige_api

titulo "Consistencia: espectro, nao interruptor"

passo "1. Estado da projecao (borda)"
curl -fsS "$API/v1/projecao" | bonito
pausa

passo "2. Dispara um Pix e le os dois saldos DURANTE o pagamento"
nota "O pagamento roda em segundo plano e a leitura comeca no mesmo instante."
nota "Se esperassemos a resposta chegar (~350ms, por causa do SPI), a projecao"
nota "ja teria alcancado o log e a janela de divergencia teria passado batido."
echo

E2E=$(curl -fsS -X POST "$API/v1/pix/e2e" | json "d['e2e_id']")

curl -fsS -o /tmp/techpix_eventual.json -X POST "$API/v1/pix/pagamentos" \
  -H 'Content-Type: application/json' \
  -d "{\"e2e_id\":\"$E2E\",\"conta_pagador\":\"carteira:ana\",\"chave_recebedor\":\"+5521988887777\",\"valor_centavos\":12345}" &
PAGAMENTO_PID=$!

# Leitura a cada ~50ms, com o relogio real de cada amostra.
python3 - "$API" <<'PY'
import json, sys, time, urllib.request

api = sys.argv[1]
inicio = time.time()
for _ in range(16):
    with urllib.request.urlopen(api + "/v1/contas/carteira:ana/saldo") as r:
        d = json.load(r)
    marca = (time.time() - inicio) * 1000
    div = d['divergencia_centavos']
    sinal = '  <-- borda atrasada' if div else ''
    print('  t+%6.0fms  forte=%-13s eventual=%-13s divergencia=%7d centavos  (nao projetados: %s)%s' % (
        marca, d['forte']['saldo'], d['eventual']['saldo'],
        div, d['lancamentos_nao_projetados'], sinal))
    time.sleep(0.05)
PY

wait $PAGAMENTO_PID
json "'  pagamento concluido: %s — %s' % (d['pagamento']['status'], d['pagamento']['valor'])" < /tmp/techpix_eventual.json
pausa

passo "3. Extrato (read model, eventual)"
curl -fsS "$API/v1/contas/carteira:ana/extrato?limite=5" | bonito

titulo "O ponto"
nota "Divergir por ~250ms na BORDA e escolha, nao defeito."
nota "Ver o extrato atrasado nao machuca. Debitar errado, sim."
nota "Por isso quem autoriza pagamento e o ledger — nunca a projecao."
echo
nota "Se as tabelas de projecao forem apagadas, o sistema se reconstroi do log."
nota "Se 'entries' for apagada, acabou o sistema. Essa assimetria e o CQRS aqui."
