package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Registro struct {
	Chave      string
	Escopo     string
	Hash       string
	Estado     string
	Status     int
	Corpo      []byte
	CriadoEm   time.Time
	AtualizadoEm time.Time
}

// Reivindicar tenta virar o dono da chave. O INSERT ... ON CONFLICT DO NOTHING
// é a primitiva: quem consegue inserir executa, quem não consegue observa.
// Não há janela entre "checar" e "inserir" — é a mesma operação atômica.
func Reivindicar(ctx context.Context, q Querier, chave, escopo, hash string) (bool, error) {
	ct, err := q.Exec(ctx, `
		INSERT INTO idempotency_keys (key, scope, request_hash, state)
		VALUES ($1, $2, $3, 'IN_PROGRESS')
		ON CONFLICT (key) DO NOTHING`, chave, escopo, hash)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// Reassumir recupera chaves órfãs: o processo morreu entre reivindicar e
// concluir. Sem isso, um crash deixaria a chave travada para sempre — e o
// cliente sem conseguir nem tentar de novo (§4.5, "crash entre debitar e confirmar").
func Reassumir(ctx context.Context, q Querier, chave string, ttl time.Duration) (bool, error) {
	ct, err := q.Exec(ctx, `
		UPDATE idempotency_keys
		   SET updated_at = now()
		 WHERE key = $1
		   AND state = 'IN_PROGRESS'
		   AND updated_at < now() - make_interval(secs => $2)`, chave, ttl.Seconds())
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

func Buscar(ctx context.Context, q Querier, chave string) (*Registro, error) {
	var r Registro
	var status *int
	err := q.QueryRow(ctx, `
		SELECT key, scope, request_hash, state, response_status, response_body, created_at, updated_at
		  FROM idempotency_keys WHERE key = $1`, chave).
		Scan(&r.Chave, &r.Escopo, &r.Hash, &r.Estado, &status, &r.Corpo, &r.CriadoEm, &r.AtualizadoEm)
	if err != nil {
		return nil, err
	}
	if status != nil {
		r.Status = *status
	}
	return &r, nil
}

// Concluir é chamado DENTRO da transação que gravou os lançamentos.
// Essa é a propriedade que faz a idempotência funcionar de verdade.
func Concluir(ctx context.Context, q Querier, chave, estado string, status int, corpo []byte) error {
	_, err := q.Exec(ctx, `
		UPDATE idempotency_keys
		   SET state = $2, response_status = $3, response_body = $4, updated_at = now()
		 WHERE key = $1`, chave, estado, status, corpo)
	return err
}

// Liberar apaga o registro após falha transitória, devolvendo ao cliente o
// direito de tentar novamente com a mesma chave.
func Liberar(ctx context.Context, q Querier, chave string) error {
	_, err := q.Exec(ctx, `DELETE FROM idempotency_keys WHERE key = $1`, chave)
	return err
}
