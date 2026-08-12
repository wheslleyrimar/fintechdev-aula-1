// Package config concentra as alavancas da arquitetura em UM lugar.
// Quase toda variável aqui é um trade-off da Aula 1 exposto como parâmetro:
// mexer nelas ao vivo é metade da aula.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port        string
	DatabaseURL string
	BacenURL    string
	// BacenPublicURL é como o NAVEGADOR enxerga o simulador. Dentro do compose
	// o serviço é "bacen-sim"; da máquina do professor é "localhost".
	BacenPublicURL string
	ISPB           string

	// Núcleo forte (ADR-001)
	DBMaxConns     int32
	TxMaxRetries   int
	LedgerLockMode string // "optimistic" (SSI) | "pessimistic" (SELECT ... FOR UPDATE)

	// DICT no caminho crítico (§6.4)
	DictTimeout     time.Duration
	DictCacheTTL    time.Duration
	DictNegCacheTTL time.Duration
	BreakerFailures int
	BreakerOpen     time.Duration

	// SPI (§6.3)
	SPITimeout time.Duration

	// Idempotência (§4.3)
	IdempotencyWait    time.Duration
	IdempotencyLockTTL time.Duration

	// Borda eventual (§5.5)
	ProjectorLag      time.Duration
	ProjectorInterval time.Duration

	// Reconciliação (§4.5)
	ReconcilerInterval time.Duration
	ReconcilerAfter    time.Duration

	// Risco / PLD-FT (§6.5, passo 3 — falhar fechado)
	PixMaxAmountCents   int64
	PixNightLimitCents  int64
	PLDFTBlocklist      []string
}

func Load() Config {
	return Config{
		Port:        env("PORT", "8080"),
		DatabaseURL: env("DATABASE_URL", "postgres://techpix:techpix@localhost:5432/techpix?sslmode=disable"),
		BacenURL:       env("BACEN_BASE_URL", "http://localhost:9090"),
		BacenPublicURL: env("BACEN_PUBLIC_URL", "http://localhost:9090"),
		ISPB:           env("TECHPIX_ISPB", "00000001"),

		DBMaxConns:     int32(envInt("DB_MAX_CONNS", 20)),
		TxMaxRetries:   envInt("TX_MAX_RETRIES", 5),
		LedgerLockMode: env("LEDGER_LOCK_MODE", "optimistic"),

		DictTimeout:     envMS("DICT_TIMEOUT_MS", 1000),
		DictCacheTTL:    time.Duration(envInt("DICT_CACHE_TTL_S", 300)) * time.Second,
		DictNegCacheTTL: time.Duration(envInt("DICT_NEG_CACHE_TTL_S", 30)) * time.Second,
		BreakerFailures: envInt("BREAKER_FAILURES", 5),
		BreakerOpen:     envMS("BREAKER_OPEN_MS", 10000),

		SPITimeout: envMS("SPI_TIMEOUT_MS", 8000),

		IdempotencyWait:    envMS("IDEMPOTENCY_WAIT_MS", 3000),
		IdempotencyLockTTL: time.Duration(envInt("IDEMPOTENCY_LOCK_TTL_S", 30)) * time.Second,

		ProjectorLag:      envMS("PROJECTOR_LAG_MS", 250),
		ProjectorInterval: envMS("PROJECTOR_INTERVAL_MS", 150),

		ReconcilerInterval: time.Duration(envInt("RECONCILER_INTERVAL_S", 5)) * time.Second,
		ReconcilerAfter:    time.Duration(envInt("RECONCILER_AFTER_S", 10)) * time.Second,

		PixMaxAmountCents:  int64(envInt("PIX_MAX_AMOUNT_CENTS", 10_000_000)),
		PixNightLimitCents: int64(envInt("PIX_NIGHT_LIMIT_CENTS", 100_000)),
		PLDFTBlocklist:     envList("PLDFT_BLOCKLIST", "99999999999"),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envMS(k string, def int) time.Duration {
	return time.Duration(envInt(k, def)) * time.Millisecond
}

func envList(k, def string) []string {
	raw := env(k, def)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
