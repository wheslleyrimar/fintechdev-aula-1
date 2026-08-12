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

type Pagamento struct {
	E2EID          string
	ContaPagador   string
	ChaveRecebedor string
	ISPBRecebedor  string
	BancoRecebedor string
	NomeRecebedor  string
	ValorCentavos  int64
	Descricao      string
	Status         string
	StatusSPI      string
	MotivoSPI      string
	TxReserva      string
	TxLiquidacao   string
	CriadoEm       time.Time
	AtualizadoEm   time.Time
}

const colunas = `e2e_id, payer_account_code, payee_key, payee_ispb, payee_bank, payee_name,
	amount_cents, description, status, COALESCE(spi_status,''), COALESCE(spi_reason,''),
	COALESCE(reserve_tx_id::text,''), COALESCE(settle_tx_id::text,''), created_at, updated_at`

// Criar grava a máquina de estados do pagamento DENTRO da mesma transação da
// reserva no ledger. Dinheiro reservado e processo registrado, ou nada.
func Criar(ctx context.Context, q Querier, p Pagamento, contaPagadorID string, orcamento []byte) error {
	_, err := q.Exec(ctx, `
		INSERT INTO pix_payments (
			e2e_id, payer_account_id, payer_account_code, payee_key, payee_ispb, payee_bank,
			payee_name, amount_cents, description, status, reserve_tx_id, budget_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11,'')::uuid, $12)`,
		p.E2EID, contaPagadorID, p.ContaPagador, p.ChaveRecebedor, p.ISPBRecebedor, p.BancoRecebedor,
		p.NomeRecebedor, p.ValorCentavos, p.Descricao, p.Status, p.TxReserva, orcamento)
	return err
}

func MarcarLiquidado(ctx context.Context, q Querier, e2e, txLiquidacao, statusSPI string) error {
	_, err := q.Exec(ctx, `
		UPDATE pix_payments
		   SET status = 'SETTLED', settle_tx_id = $2::uuid, spi_status = $3, updated_at = now()
		 WHERE e2e_id = $1`, e2e, txLiquidacao, statusSPI)
	return err
}

func MarcarEstornado(ctx context.Context, q Querier, e2e, txEstorno, statusSPI, motivo string) error {
	_, err := q.Exec(ctx, `
		UPDATE pix_payments
		   SET status = 'REFUNDED', settle_tx_id = $2::uuid, spi_status = $3, spi_reason = $4, updated_at = now()
		 WHERE e2e_id = $1`, e2e, txEstorno, statusSPI, motivo)
	return err
}

func Buscar(ctx context.Context, q Querier, e2e string) (*Pagamento, error) {
	var p Pagamento
	err := q.QueryRow(ctx, `SELECT `+colunas+` FROM pix_payments WHERE e2e_id = $1`, e2e).
		Scan(&p.E2EID, &p.ContaPagador, &p.ChaveRecebedor, &p.ISPBRecebedor, &p.BancoRecebedor,
			&p.NomeRecebedor, &p.ValorCentavos, &p.Descricao, &p.Status, &p.StatusSPI, &p.MotivoSPI,
			&p.TxReserva, &p.TxLiquidacao, &p.CriadoEm, &p.AtualizadoEm)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func Listar(ctx context.Context, q Querier, limite int) ([]Pagamento, error) {
	rows, err := q.Query(ctx, `SELECT `+colunas+` FROM pix_payments ORDER BY created_at DESC LIMIT $1`, limite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLista(rows)
}

// Pendentes lista pagamentos presos em RESERVED há mais de `idade`.
// Cada linha aqui é dinheiro parado no limbo — a fila de trabalho da
// reconciliação (§4.5).
func Pendentes(ctx context.Context, q Querier, idade time.Duration, limite int) ([]Pagamento, error) {
	rows, err := q.Query(ctx, `
		SELECT `+colunas+`
		  FROM pix_payments
		 WHERE status = 'RESERVED'
		   AND updated_at < now() - make_interval(secs => $1)
		 ORDER BY created_at
		 LIMIT $2`, idade.Seconds(), limite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLista(rows)
}

func scanLista(rows pgx.Rows) ([]Pagamento, error) {
	var out []Pagamento
	for rows.Next() {
		var p Pagamento
		if err := rows.Scan(&p.E2EID, &p.ContaPagador, &p.ChaveRecebedor, &p.ISPBRecebedor, &p.BancoRecebedor,
			&p.NomeRecebedor, &p.ValorCentavos, &p.Descricao, &p.Status, &p.StatusSPI, &p.MotivoSPI,
			&p.TxReserva, &p.TxLiquidacao, &p.CriadoEm, &p.AtualizadoEm); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func ContaIDPorCodigo(ctx context.Context, q Querier, codigo string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id::text FROM accounts WHERE code = $1`, codigo).Scan(&id)
	return id, err
}
