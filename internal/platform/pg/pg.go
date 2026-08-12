// Package pg é o acesso ao Postgres do núcleo.
//
// Aula 1 · §3.7: para o ledger, SERIALIZABLE é necessário. Read committed não
// protege invariante multi-linha; snapshot isolation ainda permite write skew.
// O Postgres implementa serializable via SSI (Serializable Snapshot Isolation):
// as transações rodam em paralelo sobre snapshots e ABORTAM se o banco detectar
// uma dependência perigosa. Abortar é normal — por isso todo caminho de escrita
// precisa de retry. É o preço explícito da correção.
package pg

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/wheslleyrimar/techpix/internal/platform/obs"
)

// Tx é o tipo de transação que atravessa os módulos.
//
// Sim, isto é infraestrutura vazando na fronteira dos módulos — e é
// DELIBERADO. O ADR-001 exige que "marcar concluído" e "gravar lançamentos"
// aconteçam na MESMA transação (§4.3, propriedade 3). Num monólito modular
// isso custa um `pg.Tx` compartilhado. Em microsserviços custaria 2PC ou saga.
// Ver ADR-002.
type Tx = pgx.Tx

type DB struct {
	Pool       *pgxpool.Pool
	MaxRetries int
}

func Open(ctx context.Context, url string, maxConns int32, maxRetries int) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL invalida: %w", err)
	}
	// Lei de Little (§3.5): L = λ × W. O pool é o L. Se a latência (W) sobe,
	// a concorrência necessária sobe junto e o pool estoura. Este número é
	// uma decisão de capacidade, não um default esquecido.
	cfg.MaxConns = maxConns
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}

	ctxPing, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if err = pool.Ping(ctxPing); err == nil {
			break
		}
		select {
		case <-ctxPing.Done():
			return nil, fmt.Errorf("postgres indisponivel: %w", err)
		case <-time.After(500 * time.Millisecond):
		}
	}

	slog.Info("postgres conectado", "max_conns", maxConns, "tx_max_retries", maxRetries)
	return &DB{Pool: pool, MaxRetries: maxRetries}, nil
}

func (db *DB) Close() { db.Pool.Close() }

// InSerializableTx roda fn sob SERIALIZABLE, com retry em falha de serialização.
//
// É AQUI que o ADR-001 vira código: todo movimento de dinheiro passa por esta
// função. Se o Postgres abortar por dependência perigosa (SQLSTATE 40001),
// tentamos de novo com backoff. O contador `ledger.serialization_retry` é o
// sinal que a Aula 2 vai usar para encontrar o "ponto quente".
func (db *DB) InSerializableTx(ctx context.Context, fn func(Tx) error) error {
	return db.inTx(ctx, pgx.Serializable, fn)
}

// InReadCommittedTx é para a BORDA (projeções, leituras). Consistência eventual
// aqui é escolha consciente: ver o extrato 200ms atrasado não machuca ninguém;
// debitar errado, sim (§5.3).
func (db *DB) InReadCommittedTx(ctx context.Context, fn func(Tx) error) error {
	return db.inTx(ctx, pgx.ReadCommitted, fn)
}

func (db *DB) inTx(ctx context.Context, iso pgx.TxIsoLevel, fn func(Tx) error) error {
	var ultimoErr error
	tentativas := db.MaxRetries
	if tentativas < 1 {
		tentativas = 1
	}

	for tentativa := 1; tentativa <= tentativas; tentativa++ {
		tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: iso})
		if err != nil {
			return err
		}

		err = fn(tx)
		if err != nil {
			_ = tx.Rollback(ctx)
			if IsSerializationFailure(err) {
				ultimoErr = err
				obs.Inc("ledger.serialization_retry")
				esperar(ctx, tentativa)
				continue
			}
			return err
		}

		if err := tx.Commit(ctx); err != nil {
			if IsSerializationFailure(err) {
				ultimoErr = err
				obs.Inc("ledger.serialization_retry")
				esperar(ctx, tentativa)
				continue
			}
			return err
		}

		if tentativa > 1 {
			slog.Warn("transacao serializada apenas apos retry", "tentativas", tentativa)
		}
		return nil
	}

	obs.Inc("ledger.serialization_exhausted")
	return fmt.Errorf("falha de serializacao apos %d tentativas (contencao alta — ver Aula 2, ponto quente): %w",
		tentativas, ultimoErr)
}

// esperar aplica backoff exponencial com jitter. Sem jitter, os retries
// re-colidem em bloco — a "tempestade de retentativas" da §3.7.
func esperar(ctx context.Context, tentativa int) {
	base := time.Duration(1<<uint(tentativa-1)) * 5 * time.Millisecond
	jitter := time.Duration(rand.Int63n(int64(base) + 1))
	select {
	case <-ctx.Done():
	case <-time.After(base + jitter):
	}
}

// IsSerializationFailure cobre 40001 (serialization_failure) e 40P01 (deadlock).
func IsSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}

// IsCheckViolation identifica as invariantes do banco (partida dobrada,
// append-only, valores positivos) sendo defendidas.
func IsCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23514"
	}
	return false
}

func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// Migrate aplica os .sql embutidos, uma vez cada, em ordem de nome.
func (db *DB) Migrate(ctx context.Context, fsys fs.FS) error {
	_, err := db.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return err
	}

	arquivos, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(arquivos)

	for _, nome := range arquivos {
		var existe bool
		if err := db.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, nome).Scan(&existe); err != nil {
			return err
		}
		if existe {
			continue
		}

		conteudo, err := fs.ReadFile(fsys, nome)
		if err != nil {
			return err
		}

		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(conteudo)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", nome, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, nome); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		slog.Info("migration aplicada", "arquivo", nome)
	}
	return nil
}
