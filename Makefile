SHELL := /bin/bash
API   ?= http://localhost:8080

.PHONY: help up painel down logs logs-bacen ps build test fitness demo demo-ana demo-dict demo-eventual demo-reconcile demo-contencao reset psql

help: ## Lista os alvos
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

up: ## Sobe postgres + bacen-sim + techpix
	docker compose up -d --build
	@echo "aguardando healthchecks..."
	@until curl -fsS $(API)/healthz >/dev/null 2>&1; do sleep 1; done
	@echo ""
	@echo "  TechPix pronto."
	@echo "  Painel da aula:  $(API)"
	@echo "  BACEN simulado:  http://localhost:9090/admin/config"
	@echo ""

painel: ## Abre o painel da aula no navegador
	@open $(API) 2>/dev/null || xdg-open $(API) 2>/dev/null || echo "abra $(API)"

down: ## Derruba tudo (mantém o volume do banco)
	docker compose down

reset: ## Derruba tudo e APAGA o banco
	docker compose down -v

logs: ## Logs do monólito
	docker compose logs -f techpix

logs-bacen: ## Logs do simulador do BACEN
	docker compose logs -f bacen-sim

ps: ## Status dos containers
	docker compose ps

build: ## Compila local (sem docker)
	go build ./...

test: ## Harness: roda as fitness functions contra o Postgres do compose
	docker compose --profile test run --rm tester

fitness: ## Invariantes verificadas ao vivo, contra o banco de produção do demo
	@curl -fsS $(API)/v1/fitness | python3 -m json.tool

demo: demo-ana ## Demo principal (os três toques da Ana)

demo-ana: ## §4 — Ana toca "pagar" 3x. Um débito, três respostas.
	@./scripts/demo_ana.sh

demo-dict: ## §6.4 — token bucket do DICT: 404 custa 20 tokens, depois 429.
	@./scripts/demo_dict.sh

demo-eventual: ## §5.5 — forte no núcleo, eventual na borda (lag visível).
	@./scripts/demo_eventual.sh

demo-reconcile: ## §4.5 — resposta do SPI se perde; reconciliação por E2E ID resolve.
	@./scripts/demo_reconciliacao.sh

demo-contencao: ## §3.7 — escrita concorrente na mesma conta: SSI, retries, ponto quente.
	@./scripts/demo_contencao.sh

psql: ## Abre psql no ledger
	docker compose exec postgres psql -U techpix -d techpix
