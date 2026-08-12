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

// LancamentoBruto é uma linha do LOG a ser projetada.
// A projeção lê o log; o log nunca lê a projeção.
type LancamentoBruto struct {
	ID            int64
	ContaID       string
	ContaCodigo   string
	Natureza      string
	TransacaoID   string
	E2EID         string
	Tipo          string
	Descricao     string
	Direcao       string
	ValorCentavos int64
	OcorridoEm    time.Time
}

func Cursor(ctx context.Context, q Querier, nome string) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, `SELECT last_entry_id FROM projection_cursor WHERE name = $1`, nome).Scan(&id)
	if err == pgx.ErrNoRows {
		_, err = q.Exec(ctx, `INSERT INTO projection_cursor (name, last_entry_id) VALUES ($1, 0)`, nome)
		return 0, err
	}
	return id, err
}

func AvancarCursor(ctx context.Context, q Querier, nome string, id int64) error {
	_, err := q.Exec(ctx,
		`UPDATE projection_cursor SET last_entry_id = $2, updated_at = now() WHERE name = $1`, nome, id)
	return err
}

func LancamentosApos(ctx context.Context, q Querier, cursor int64, limite int) ([]LancamentoBruto, error) {
	rows, err := q.Query(ctx, `
		SELECT e.id, e.account_id::text, a.code, a.kind, t.id::text, COALESCE(t.e2e_id,''),
		       t.kind, t.description, e.direction, e.amount_cents, t.occurred_at
		  FROM entries e
		  JOIN accounts a     ON a.id = e.account_id
		  JOIN transactions t ON t.id = e.transaction_id
		 WHERE e.id > $1
		 ORDER BY e.id
		 LIMIT $2`, cursor, limite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LancamentoBruto
	for rows.Next() {
		var l LancamentoBruto
		if err := rows.Scan(&l.ID, &l.ContaID, &l.ContaCodigo, &l.Natureza, &l.TransacaoID,
			&l.E2EID, &l.Tipo, &l.Descricao, &l.Direcao, &l.ValorCentavos, &l.OcorridoEm); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func SaldoProjetado(ctx context.Context, q Querier, contaID string) (int64, error) {
	var saldo int64
	err := q.QueryRow(ctx,
		`SELECT COALESCE(balance_cents, 0) FROM balances_projection WHERE account_id = $1`, contaID).Scan(&saldo)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	return saldo, err
}

func AplicarSaldo(ctx context.Context, q Querier, contaID string, saldo, ultimoLancamento int64) error {
	_, err := q.Exec(ctx, `
		INSERT INTO balances_projection (account_id, balance_cents, last_entry_id, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (account_id) DO UPDATE
		   SET balance_cents = EXCLUDED.balance_cents,
		       last_entry_id = EXCLUDED.last_entry_id,
		       updated_at    = now()`, contaID, saldo, ultimoLancamento)
	return err
}

func InserirExtrato(ctx context.Context, q Querier, l LancamentoBruto, assinado, saldoApos int64) error {
	var e2e *string
	if l.E2EID != "" {
		e2e = &l.E2EID
	}
	_, err := q.Exec(ctx, `
		INSERT INTO statement_entries (
			entry_id, account_id, transaction_id, e2e_id, kind, description,
			direction, amount_cents, signed_cents, balance_after_cents, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (entry_id) DO NOTHING`,
		l.ID, l.ContaID, l.TransacaoID, e2e, l.Tipo, l.Descricao,
		l.Direcao, l.ValorCentavos, assinado, saldoApos, l.OcorridoEm)
	return err
}

type Projecao struct {
	ContaID       string
	SaldoCentavos int64
	UltimoLancamento int64
	AtualizadoEm  time.Time
}

func BuscarProjecao(ctx context.Context, q Querier, codigo string) (*Projecao, error) {
	var p Projecao
	err := q.QueryRow(ctx, `
		SELECT a.id::text, COALESCE(b.balance_cents,0), COALESCE(b.last_entry_id,0),
		       COALESCE(b.updated_at, now())
		  FROM accounts a
		  LEFT JOIN balances_projection b ON b.account_id = a.id
		 WHERE a.code = $1`, codigo).
		Scan(&p.ContaID, &p.SaldoCentavos, &p.UltimoLancamento, &p.AtualizadoEm)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

type LinhaExtrato struct {
	LancamentoID  int64
	E2EID         string
	Tipo          string
	Descricao     string
	Direcao       string
	ValorCentavos int64
	SaldoApos     int64
	OcorridoEm    time.Time
	ProjetadoEm   time.Time
}

func Extrato(ctx context.Context, q Querier, codigo string, limite int) ([]LinhaExtrato, error) {
	rows, err := q.Query(ctx, `
		SELECT s.entry_id, COALESCE(s.e2e_id,''), s.kind, s.description, s.direction,
		       s.amount_cents, s.balance_after_cents, s.occurred_at, s.projected_at
		  FROM statement_entries s
		  JOIN accounts a ON a.id = s.account_id
		 WHERE a.code = $1
		 ORDER BY s.entry_id DESC
		 LIMIT $2`, codigo, limite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LinhaExtrato
	for rows.Next() {
		var l LinhaExtrato
		if err := rows.Scan(&l.LancamentoID, &l.E2EID, &l.Tipo, &l.Descricao, &l.Direcao,
			&l.ValorCentavos, &l.SaldoApos, &l.OcorridoEm, &l.ProjetadoEm); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Defasagem: quantos lançamentos do log ainda não chegaram à projeção.
// É a métrica da consistência eventual — o "quanto atrás" da borda.
func Defasagem(ctx context.Context, q Querier) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM entries
		 WHERE id > (SELECT last_entry_id FROM projection_cursor WHERE name = 'statement')`).Scan(&n)
	return n, err
}
