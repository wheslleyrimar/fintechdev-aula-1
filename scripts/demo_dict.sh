#!/usr/bin/env bash
# =============================================================================
# Aula 1 · §6.4 — DICT: o diretorio protegido por token bucket.
#
#   HTTP 200 (chave existe)     -> custa  1 token
#   HTTP 404 (chave nao existe) -> custa 20 tokens   <- punicao a varredura
#   balde vazio                 -> HTTP 429
#
# "Design de seguranca por incentivo": o custo assimetrico pune scraping sem
# punir uso normal.
# =============================================================================
set -euo pipefail
cd "$(dirname "$0")"
source ./_lib.sh
exige_api

titulo "DICT: chave e recurso caro"

passo "1. Politica atual do simulador"
curl -fsS "$BACEN/admin/config" | bonito

passo "2. Balde zerado para comecar limpo"
curl -fsS -X POST "$BACEN/admin/config" -H 'Content-Type: application/json' \
  -d '{"zerar_baldes":true,"dict_tokens_por_minuto":60,"dict_capacidade":100}' >/dev/null
ok "baldes reiniciados (capacidade 100, reposicao 60/min)"
pausa

passo "3. Consulta valida: custa 1 token"
curl -fsS "$API/v1/bacen/dict/bruno@bancobeta.com.br" | bonito
nota "A segunda consulta a MESMA chave nem chega ao DICT: cache."
curl -fsS "$API/v1/bacen/dict/bruno@bancobeta.com.br" | json "'do_cache = %s' % d['chave']['do_cache']"
pausa

passo "4. Chave malformada: bloqueada LOCALMENTE, sem gastar token"
curl -sS -o /dev/null -w "  HTTP %{http_code}\n" "$API/v1/bacen/dict/12345678900"
nota "CPF com digito verificador invalido. Nunca existiria no DICT."
nota "Consultar teria queimado 20 tokens a toa. Validacao local e economia."
pausa

passo "5. Varredura: chaves inexistentes (bem formadas) a 20 tokens cada"
for i in $(seq 1 6); do
  CODIGO=$(curl -sS -o /dev/null -w "%{http_code}" "$API/v1/bacen/dict/naoexiste$i@fantasma.com")
  RESTANTES=$(curl -fsS "$BACEN/dict/v1/baldes" \
    | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['baldes'].get('00000001',{}).get('tokens_disponiveis','?'))")
  printf "  consulta %d -> HTTP %-3s | tokens restantes: %s\n" "$i" "$CODIGO" "$RESTANTES"
done
nota "5 consultas 404 derrubam um balde de 100 tokens. Cinco."
pausa

passo "6. Estado dos baldes no BACEN"
curl -fsS "$BACEN/dict/v1/baldes" | bonito

passo "7. Insistindo depois do balde vazio"
for i in $(seq 7 14); do
  CODIGO=$(curl -sS -o /dev/null -w "%{http_code}" "$API/v1/bacen/dict/naoexiste$i@fantasma.com")
  printf "  consulta %2d -> HTTP %s\n" "$i" "$CODIGO"
done
nota "429 = balde esgotado no BACEN."
nota "503 = o CIRCUIT BREAKER do TechPix abriu e parou de bater no DICT."
nota "Falhar rapido em casa e melhor que insistir com quem ja disse nao —"
nota "cada requisicao pendurada segura uma conexao e aproxima o pool do limite."
pausa

passo "8. Estado do circuit breaker e do cache do lado do TechPix"
curl -fsS "$API/v1/bacen/estado" | bonito

titulo "Consequencias de design"
nota "· cache disciplinado (positivo E negativo)"
nota "· validacao local antes de consultar"
nota "· consolidacao de consultas; evitar 404 a todo custo"
nota "· timeout curto + circuit breaker: um solucco no DICT nao pode derrubar a fintech"
