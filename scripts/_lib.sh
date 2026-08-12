#!/usr/bin/env bash
# Utilidades compartilhadas pelos scripts de demonstração.
# Nada de jq: só bash + python3, que já existe em qualquer máquina da turma.

API=${API:-http://localhost:8080}
BACEN=${BACEN:-http://localhost:9090}

NEGRITO=$'\033[1m'; VERDE=$'\033[32m'; AMARELO=$'\033[33m'; VERMELHO=$'\033[31m'; AZUL=$'\033[36m'; FIM=$'\033[0m'

titulo()  { printf "\n%s══ %s ══%s\n" "$NEGRITO$AZUL" "$1" "$FIM"; }
passo()   { printf "\n%s▸ %s%s\n" "$NEGRITO" "$1" "$FIM"; }
nota()    { printf "  %s%s%s\n" "$AMARELO" "$1" "$FIM"; }
ok()      { printf "  %s✓ %s%s\n" "$VERDE" "$1" "$FIM"; }
falha()   { printf "  %s✗ %s%s\n" "$VERMELHO" "$1" "$FIM"; }
pausa()   { [ "${SEM_PAUSA:-0}" = "1" ] || { printf "\n%s[enter para continuar]%s" "$AZUL" "$FIM"; read -r _; }; }

# json <expressao-python> — lê JSON do stdin e imprime um campo.
#   ... | json "d['pagamento']['status']"
json() { python3 -c "import sys,json; d=json.load(sys.stdin); print($1)"; }

bonito() { python3 -m json.tool; }

exige_api() {
  if ! curl -fsS "$API/healthz" >/dev/null 2>&1; then
    falha "TechPix nao responde em $API — rode 'make up' primeiro"
    exit 1
  fi
}
