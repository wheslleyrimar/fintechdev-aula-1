// Comando techpix: o MONÓLITO MODULAR da Aula 1.
//
// Um processo. Um deploy. Um banco. Seis módulos com fronteiras reais:
//
//	ledger       núcleo forte, append-only, partida dobrada        (§3, ADR-001)
//	idempotency  efeito exactly-once via EndToEndId                (§4)
//	bacen        camada anticorrupção com DICT e SPI               (§6)
//	pix          orquestração do pagamento ponta a ponta           (§6.5)
//	accounts     contas de cliente e chaves locais
//	statement    read model eventual (CQRS na borda)               (§5.5)
//
// A régua de evolução no fim da Aula 1 diz exatamente isto: "Monólito TechPix
// (uma app, um deploy)". Microsserviço aqui seria complexidade sem problema.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/wheslleyrimar/techpix/internal/modules/accounts"
	"github.com/wheslleyrimar/techpix/internal/modules/bacen"
	"github.com/wheslleyrimar/techpix/internal/modules/idempotency"
	"github.com/wheslleyrimar/techpix/internal/modules/ledger"
	"github.com/wheslleyrimar/techpix/internal/modules/pix"
	"github.com/wheslleyrimar/techpix/internal/modules/statement"
	"github.com/wheslleyrimar/techpix/internal/modules/ui"
	"github.com/wheslleyrimar/techpix/internal/platform/config"
	"github.com/wheslleyrimar/techpix/internal/platform/httpx"
	"github.com/wheslleyrimar/techpix/internal/platform/modular"
	"github.com/wheslleyrimar/techpix/internal/platform/obs"
	"github.com/wheslleyrimar/techpix/internal/platform/pg"
	"github.com/wheslleyrimar/techpix/migrations"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg := config.Load()
	ctx, cancelar := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelar()

	db, err := pg.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, cfg.TxMaxRetries)
	if err != nil {
		slog.Error("nao foi possivel abrir o banco", "erro", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Migrate(ctx, migrations.FS); err != nil {
		slog.Error("migrations falharam", "erro", err)
		os.Exit(1)
	}

	// --- Composição do monólito: as dependências entre módulos aparecem AQUI,
	// explicitamente, em uma tela. Se ficar difícil de ler, a arquitetura
	// azedou — e a gente descobre cedo.
	modLedger := ledger.New(db, cfg.LedgerLockMode)
	modIdem := idempotency.New(db, cfg.IdempotencyWait, cfg.IdempotencyLockTTL)
	modBacen := bacen.New(bacen.Opcoes{
		BaseURL:       cfg.BacenURL,
		ISPB:          cfg.ISPB,
		DictTimeout:   cfg.DictTimeout,
		DictTTL:       cfg.DictCacheTTL,
		DictTTLNeg:    cfg.DictNegCacheTTL,
		BreakerFalhas: cfg.BreakerFailures,
		BreakerAberto: cfg.BreakerOpen,
		SPITimeout:    cfg.SPITimeout,
	})
	modStatement := statement.New(db, modLedger, cfg.ProjectorLag, cfg.ProjectorInterval)
	modAccounts := accounts.New(db, modLedger, modIdem)
	modPix := pix.New(db, modLedger, modIdem, modBacen, modBacen, pix.Opcoes{
		ISPB:                  cfg.ISPB,
		ValorMaximoCentavos:   cfg.PixMaxAmountCents,
		LimiteNoturnoCentavos: cfg.PixNightLimitCents,
		Blocklist:             cfg.PLDFTBlocklist,
		ReconcIntervalo:       cfg.ReconcilerInterval,
		ReconcIdade:           cfg.ReconcilerAfter,
	})

	reg := &modular.Registry{}
	reg.Registrar(ui.New())
	reg.Registrar(modLedger)
	reg.Registrar(modIdem)
	reg.Registrar(modBacen)
	reg.Registrar(modAccounts)
	reg.Registrar(modPix)
	reg.Registrar(modStatement)

	mux := http.NewServeMux()
	reg.Montar(mux)
	rotasDePlataforma(mux, reg, cfg)

	reg.Iniciar(ctx) // projeção e reconciliação de fundo

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpx.Middleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		// Maior que o teto do Pix (40s) de propósito: o servidor não pode
		// cortar uma liquidação em curso por impaciência própria.
		WriteTimeout: 60 * time.Second,
		ReadTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("TechPix no ar",
			"porta", cfg.Port, "modulos", reg.Nomes(), "ispb", cfg.ISPB,
			"modo_lock", cfg.LedgerLockMode, "versao", versao())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("servidor caiu", "erro", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("desligando: parando de aceitar novas ordens de pagamento")

	desligar, cancelarDesligar := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelarDesligar()
	if err := srv.Shutdown(desligar); err != nil {
		slog.Error("desligamento forcado", "erro", err)
	}
	slog.Info("TechPix desligado")
}

func rotasDePlataforma(mux *http.ServeMux, reg *modular.Registry, cfg config.Config) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		checks := reg.Check(r.Context())
		status := http.StatusOK
		for _, v := range checks {
			if v != "ok" {
				status = http.StatusServiceUnavailable
			}
		}
		httpx.JSON(w, status, map[string]any{"modulos": checks})
	})

	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		obs.WriteProm(w)
	})

	// /v1/latencia: latência como DISTRIBUIÇÃO (p50/p99/p99.9), nunca média.
	// Em fintech a cauda manda: é nela que o cliente desiste e o retry nasce.
	mux.HandleFunc("GET /v1/latencia", func(w http.ResponseWriter, r *http.Request) {
		stats, contadores := obs.Snapshot()
		httpx.JSON(w, http.StatusOK, map[string]any{
			"quantis_por_passo": stats,
			"contadores":        contadores,
			"referencias": map[string]any{
				"teto_normativo_pix_ms": obs.TetoNormativoPix.Milliseconds(),
				"spi_real_p50_ms":       2800,
				"spi_real_p99_ms":       4600,
				"dict_sla_p99_ms":       1000,
			},
		})
	})

	// /v1/info descreve o sistema para quem chega — inclusive para o painel web,
	// que lê daqui o endereço público do simulador do BACEN.
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]any{
			"sistema":  "TechPix — Fintech Dev, Aula 1",
			"forma":    "monolito modular (uma app, um deploy)",
			"modulos":  reg.Nomes(),
			"decisoes": []string{
				"ADR-001: ledger ACID, serializable, idempotente por EndToEndId",
				"ADR-002: monolito modular com fronteiras verificadas pelo compilador",
			},
			"bacen_publico": cfg.BacenPublicURL,
			"ispb":          cfg.ISPB,
			"config_visivel": map[string]any{
				"modo_lock_ledger":       cfg.LedgerLockMode,
				"pool_conexoes":          cfg.DBMaxConns,
				"dict_timeout_ms":        cfg.DictTimeout.Milliseconds(),
				"spi_timeout_ms":         cfg.SPITimeout.Milliseconds(),
				"atraso_projecao_ms":     cfg.ProjectorLag.Milliseconds(),
				"idempotencia_espera_ms": cfg.IdempotencyWait.Milliseconds(),
				"reconciliacao_apos_s":   int(cfg.ReconcilerAfter.Seconds()),
				"limite_noturno_centavos": cfg.PixNightLimitCents,
			},
			"rotas": map[string]string{
				"POST /v1/pix/e2e":                  "gera um EndToEndId (em producao nasce no app)",
				"POST /v1/pix/pagamentos":           "executa um Pix (idempotente pelo E2E ID)",
				"GET  /v1/pix/pagamentos/{e2e}":     "estado de um pagamento",
				"GET  /v1/contas":                   "clientes e chaves",
				"POST /v1/contas/{codigo}/depositos": "cash-in (partida dobrada)",
				"GET  /v1/contas/{codigo}/saldo":    "saldo FORTE e saldo EVENTUAL lado a lado",
				"GET  /v1/contas/{codigo}/extrato":  "extrato (read model)",
				"GET  /v1/ledger/contas":            "plano de contas com saldos",
				"GET  /v1/ledger/e2e/{e2e}":         "as transacoes de um Pix (reserva + liquidacao)",
				"GET  /v1/fitness":                  "invariantes verificadas ao vivo (Harness)",
				"GET  /v1/latencia":                 "orcamento de latencia: p50/p99/p999",
				"GET  /v1/bacen/estado":             "cache do DICT e estado do circuit breaker",
				"GET  /v1/projecao":                 "defasagem da borda eventual",
			},
		})
	})
}

func versao() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return "dev"
}
