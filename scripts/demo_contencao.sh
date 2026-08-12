#!/usr/bin/env bash
# =============================================================================
# Aula 1 · §3.7 — Isolamento e concorrencia: o ponto quente nasce aqui.
#
# 12 pagamentos SIMULTANEOS da mesma conta, cada um com E2E ID proprio (ou
# seja: intencoes DIFERENTES, sem idempotencia para salvar). Todos disputam a
# mesma linha logica de saldo.
#
# O que observar:
#   · nenhum estouro de saldo, aconteca o que acontecer
#   · o contador ledger.serialization_retry subindo (SSI abortando e retentando)
#   · a latencia p99 da reserva sofrendo com a contencao
# =============================================================================
set -euo pipefail
cd "$(dirname "$0")"
source ./_lib.sh
exige_api

titulo "Contencao no ledger: o gargalo e coordenacao, nao IOPS"

passo "1. Preparando o cenario na conta da Carla"
curl -fsS -X POST "$API/v1/contas/carteira:carla/depositos" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: setup-contencao-$(date +%s)" \
  -d '{"valor_centavos":30000,"descricao":"preparacao da demo"}' >/dev/null

SALDO=$(curl -fsS "$API/v1/contas/carteira:carla/saldo" | json "d['forte']['saldo_centavos']")
VALOR=$(( SALDO / 6 ))
ok "saldo forte: $SALDO centavos"
nota "Vamos disparar 12 pagamentos de $VALOR centavos ao mesmo tempo."
nota "So cabem 6. Os outros 6 TEM que ser recusados — nem um centavo a mais."

passo "2. Contadores ANTES"
curl -fsS "$API/v1/latencia" | python3 -c "
import sys, json
d = json.load(sys.stdin)
c = d['contadores']
print('  retries de serializacao :', c.get('ledger.serialization_retry', 0))
print('  transacoes registradas  :', c.get('ledger.transacao_registrada', 0))
"
pausa

passo "3. Disparando 12 pagamentos SIMULTANEOS da mesma conta"
for i in $(seq 1 12); do
  (
    E2E=$(curl -fsS -X POST "$API/v1/pix/e2e" | json "d['e2e_id']")
    CODIGO=$(curl -sS -o "/tmp/techpix_cont_$i.json" -w "%{http_code}" -X POST "$API/v1/pix/pagamentos" \
      -H 'Content-Type: application/json' \
      -d "{\"e2e_id\":\"$E2E\",\"conta_pagador\":\"carteira:carla\",\"chave_recebedor\":\"bruno@bancobeta.com.br\",\"valor_centavos\":$VALOR}")
    MOTIVO=$(python3 -c "
import json
d = json.load(open('/tmp/techpix_cont_$i.json'))
print(d.get('erro',{}).get('codigo') or (d.get('pagamento') or {}).get('status',''))
" 2>/dev/null || echo "?")
    printf "  pagamento %2d -> HTTP %-3s %s\n" "$i" "$CODIGO" "$MOTIVO"
  ) &
done
wait
pausa

passo "4. Contadores DEPOIS"
curl -fsS "$API/v1/latencia" | python3 -c "
import sys, json
d = json.load(sys.stdin)
c = d['contadores']
q = d['quantis_por_passo']
print('  retries de serializacao :', c.get('ledger.serialization_retry', 0))
print('  saldo insuficiente      :', c.get('ledger.saldo_insuficiente', 0))
print('  transacoes registradas  :', c.get('ledger.transacao_registrada', 0))
print()
for passo in ('4_ledger_reserva', '5_spi_liquidacao', '2_dict_consulta'):
    s = q.get(passo)
    if s:
        print('  %-20s p50=%8.2fms  p99=%8.2fms  p99.9=%8.2fms' % (passo, s['p50_ms'], s['p99_ms'], s['p999_ms']))
"
pausa

passo "5. O saldo estourou?"
curl -fsS "$API/v1/contas/carteira:carla/saldo" | bonito

passo "6. Invariantes"
curl -fsS "$API/v1/fitness" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print('  aprovado:', d['aprovado'])
for c in d['checks']:
    print('  %s %-22s %s' % ('OK ' if c['aprovado'] else 'FALHOU', c['nome'], c['detalhe']))
"

titulo "Para discutir"
nota "· Troque LEDGER_LOCK_MODE para 'pessimistic' no docker-compose.yml e repita."
nota "  Otimista (SSI): ninguem espera lock, mas aparecem abortos e retentativas."
nota "  Pessimista (FOR UPDATE): ninguem perde trabalho, mas forma-se FILA."
nota "· 2.700 escritas/s sao troco para um NVMe (500k-1M IOPS)."
nota "  O gargalo e COORDENACAO, nao capacidade bruta."
nota "· Particionar por hash(conta_id) distribui a contencao — ao custo de"
nota "  transacoes inter-particao (2PC ou saga). Aula 2 mede isso."
