#!/usr/bin/env bash
# =============================================================================
# Aula 1 · §1 e §4 — "A Ana pagou uma vez, tres vezes, ou nenhuma?"
#
# Ana tenta pagar R$ 5.000 por Pix. A tela gira. Ela toca "pagar" tres vezes.
# Este script reproduz exatamente isso — com as tres requisicoes SIMULTANEAS,
# que e o caso dificil (o retry chega ANTES do primeiro terminar).
# =============================================================================
set -euo pipefail
cd "$(dirname "$0")"
source ./_lib.sh
exige_api

titulo "Os tres toques da Ana"

passo "1. Saldo da Ana ANTES (consistencia forte: soma sobre o log)"
curl -fsS "$API/v1/contas/carteira:ana/saldo" | bonito

passo "2. O EndToEndId nasce no CLIENTE e sobrevive aos retries"
E2E=$(curl -fsS -X POST "$API/v1/pix/e2e" | json "d['e2e_id']")
nota "E2E ID = $E2E"
nota "Prefixo E + ISPB(8) + AAAAMMDDHHMM(12) + 11 alfanumericos = 32 caracteres."
nota "Se este identificador nascesse no servidor, cada toque teria uma chave"
nota "diferente e a deduplicacao seria uma ilusao."
pausa

passo "3. Ana toca 'pagar' TRES VEZES, ao mesmo tempo, com a MESMA chave"
CORPO=$(cat <<JSON
{"e2e_id":"$E2E","conta_pagador":"carteira:ana","chave_recebedor":"bruno@bancobeta.com.br","valor_centavos":500000,"descricao":"Black Friday"}
JSON
)

rm -f /tmp/techpix_toque_*.json
for i in 1 2 3; do
  curl -sS -o "/tmp/techpix_toque_$i.json" -w "  toque $i -> HTTP %{http_code} em %{time_total}s\n" \
    -X POST "$API/v1/pix/pagamentos" -H 'Content-Type: application/json' -d "$CORPO" &
done
wait

for i in 1 2 3; do
  echo
  nota "resposta do toque $i:"
  python3 -c "
import json
d = json.load(open('/tmp/techpix_toque_$i.json'))
p = d.get('pagamento') or {}
print('    status do pagamento :', p.get('status'))
print('    replay              :', d.get('replay'))
print('    valor               :', p.get('valor'))
print('    tx de reserva       :', p.get('tx_reserva'))
print('    observacao          :', d.get('observacao') or '-')
"
done
pausa

passo "4. E no ledger? Quantos debitos existem para este E2E ID?"
curl -fsS "$API/v1/ledger/e2e/$E2E" | bonito
nota "Duas transacoes: RESERVA e LIQUIDACAO. Cada uma com Σ debitos = Σ creditos."
nota "Nenhuma delas aparece duas vezes: o indice unico (e2e_id, kind) nao deixa."
pausa

passo "5. Saldo da Ana DEPOIS"
curl -fsS "$API/v1/contas/carteira:ana/saldo" | bonito

passo "6. Estado final do pagamento"
curl -fsS "$API/v1/pix/pagamentos/$E2E" | bonito

titulo "Resposta da pergunta da aula"
ok "Ana tocou 3x. Aconteceu 1x. Foi respondida 3x."
nota "Isso nao foi resolvido no codigo da tela. Foi resolvido na arquitetura:"
nota "  · a chave identifica a INTENCAO, nao a tentativa"
nota "  · o registro tem ESTADO (em andamento / concluido)"
nota "  · o efeito no ledger e ATOMICO com o registro da chave"
echo
nota "Veja o orcamento de latencia de cada passo:  curl -s $API/v1/latencia | python3 -m json.tool"
