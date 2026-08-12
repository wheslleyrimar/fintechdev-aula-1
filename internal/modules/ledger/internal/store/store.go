// Package store guarda o SQL do ledger.
//
// Está sob `ledger/internal/` de propósito: o compilador de Go impede que
// qualquer outro módulo importe daqui. Ninguém escreve em `entries` pelas
// costas do ledger. Fronteira de módulo checada em tempo de compilação.
package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/wheslleyrimar/techpix/internal/platform/money"
)

// Querier aceita tanto o pool quanto uma transação em curso.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Conta struct {
	ID         string
	Codigo     string
	Nome       string
	Natureza   string
	PermiteNeg bool
	Titular    string
}

type Lancamento struct {
	ID       int64
	Conta    string
	Direcao  string
	Valor    money.Cents
}

type Transacao struct {
	ID          string
	E2EID       string
	Tipo        string
	Descricao   string
	OcorridoEm  time.Time
	Lancamentos []Lancamento
}

// CarregarContas resolve códigos -> contas.
//
// `pessimista=true` adiciona SELECT ... FOR UPDATE: trava as linhas ANTES de
// mexer. Simples de raciocinar e nenhum trabalho é perdido, mas cria fila sob
// contenção — é a origem literal do "ponto quente" (§3.7).
// `pessimista=false` confia no SSI do Postgres: ninguém espera lock, mas sob
// contenção alta aparecem abortos 40001 e retentativas.
// As duas estratégias estão aqui para serem COMPARADAS ao vivo (LEDGER_LOCK_MODE).
func CarregarContas(ctx context.Context, q Querier, codigos []string, pessimista bool) (map[string]Conta, error) {
	sql := `SELECT id::text, code, name, kind, allow_negative, COALESCE(owner_name,'')
	          FROM accounts
	         WHERE code = ANY($1)
	         ORDER BY code` // ORDER BY estável evita deadlock entre transações
	if pessimista {
		sql += ` FOR UPDATE`
	}

	rows, err := q.Query(ctx, sql, codigos)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]Conta, len(codigos))
	for rows.Next() {
		var c Conta
		if err := rows.Scan(&c.ID, &c.Codigo, &c.Nome, &c.Natureza, &c.PermiteNeg, &c.Titular); err != nil {
			return nil, err
		}
		out[c.Codigo] = c
	}
	return out, rows.Err()
}

func InserirTransacao(ctx context.Context, q Querier, id, e2e, tipo, descricao string) error {
	var e2ePtr *string
	if e2e != "" {
		e2ePtr = &e2e
	}
	_, err := q.Exec(ctx,
		`INSERT INTO transactions (id, e2e_id, kind, description) VALUES ($1, $2, $3, $4)`,
		id, e2ePtr, tipo, descricao)
	return err
}

func InserirLancamento(ctx context.Context, q Querier, txID, contaID, direcao string, valor money.Cents) (int64, error) {
	var id int64
	err := q.QueryRow(ctx,
		`INSERT INTO entries (transaction_id, account_id, direction, amount_cents)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		txID, contaID, direcao, int64(valor)).Scan(&id)
	return id, err
}

// SaldoPorID é a fonte da verdade do saldo: uma SOMA sobre o log.
// Não existe coluna de saldo no write model. Por decisão.
func SaldoPorID(ctx context.Context, q Querier, contaID string) (money.Cents, error) {
	var v int64
	err := q.QueryRow(ctx, `SELECT COALESCE(account_balance($1), 0)`, contaID).Scan(&v)
	return money.Cents(v), err
}

func SaldoPorCodigo(ctx context.Context, q Querier, codigo string) (string, money.Cents, error) {
	var id string
	var v int64
	err := q.QueryRow(ctx,
		`SELECT a.id::text, COALESCE(account_balance(a.id), 0) FROM accounts a WHERE a.code = $1`,
		codigo).Scan(&id, &v)
	return id, money.Cents(v), err
}

func ListarContas(ctx context.Context, q Querier) ([]Conta, []money.Cents, error) {
	rows, err := q.Query(ctx,
		`SELECT a.id::text, a.code, a.name, a.kind, a.allow_negative, COALESCE(a.owner_name,''),
		        COALESCE(account_balance(a.id), 0)
		   FROM accounts a ORDER BY a.code`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var contas []Conta
	var saldos []money.Cents
	for rows.Next() {
		var c Conta
		var s int64
		if err := rows.Scan(&c.ID, &c.Codigo, &c.Nome, &c.Natureza, &c.PermiteNeg, &c.Titular, &s); err != nil {
			return nil, nil, err
		}
		contas = append(contas, c)
		saldos = append(saldos, money.Cents(s))
	}
	return contas, saldos, rows.Err()
}

// ListarTransacoes devolve as mais recentes primeiro. É o "feed do log" que a
// interface mostra ao vivo: cada linha é um fato econômico que já aconteceu e
// nunca mais vai mudar.
func ListarTransacoes(ctx context.Context, q Querier, limite int) ([]Transacao, error) {
	rows, err := q.Query(ctx, `
		SELECT t.id::text, COALESCE(t.e2e_id,''), t.kind, t.description, t.occurred_at,
		       e.id, a.code, e.direction, e.amount_cents
		  FROM transactions t
		  JOIN entries  e ON e.transaction_id = t.id
		  JOIN accounts a ON a.id = e.account_id
		 WHERE t.id IN (SELECT id FROM transactions ORDER BY created_at DESC LIMIT $1)
		 ORDER BY t.occurred_at DESC, t.id, e.id`, limite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return montar(rows)
}

func BuscarTransacoes(ctx context.Context, q Querier, where string, arg any) ([]Transacao, error) {
	rows, err := q.Query(ctx,
		`SELECT t.id::text, COALESCE(t.e2e_id,''), t.kind, t.description, t.occurred_at,
		        e.id, a.code, e.direction, e.amount_cents
		   FROM transactions t
		   JOIN entries  e ON e.transaction_id = t.id
		   JOIN accounts a ON a.id = e.account_id
		  WHERE `+where+`
		  ORDER BY t.occurred_at, t.id, e.id`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return montar(rows)
}

func montar(rows pgx.Rows) ([]Transacao, error) {
	var out []Transacao
	idx := map[string]int{}
	for rows.Next() {
		var (
			id, e2e, tipo, desc string
			ocorrido            time.Time
			l                   Lancamento
			valor               int64
		)
		if err := rows.Scan(&id, &e2e, &tipo, &desc, &ocorrido, &l.ID, &l.Conta, &l.Direcao, &valor); err != nil {
			return nil, err
		}
		l.Valor = money.Cents(valor)

		i, ok := idx[id]
		if !ok {
			out = append(out, Transacao{ID: id, E2EID: e2e, Tipo: tipo, Descricao: desc, OcorridoEm: ocorrido})
			i = len(out) - 1
			idx[id] = i
		}
		out[i].Lancamentos = append(out[i].Lancamentos, l)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Fitness functions (§7.4): as invariantes do domínio consultadas como dados.
// ---------------------------------------------------------------------------

// TransacoesDesbalanceadas deve ser SEMPRE zero. Se não for, dinheiro foi
// criado ou destruído — incidente regulatório, não bug de app.
func TransacoesDesbalanceadas(ctx context.Context, q Querier) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT transaction_id
			  FROM entries
			 GROUP BY transaction_id
			HAVING COALESCE(SUM(amount_cents) FILTER (WHERE direction='D'),0)
			    <> COALESCE(SUM(amount_cents) FILTER (WHERE direction='C'),0)
		) x`).Scan(&n)
	return n, err
}

// ContasNegativas: saldo negativo em conta que não pode ficar negativa.
func ContasNegativas(ctx context.Context, q Querier) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT a.code
		  FROM accounts a
		 WHERE a.allow_negative = false
		   AND COALESCE(account_balance(a.id), 0) < 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SomaGlobal: a conservação do dinheiro no sistema inteiro.
// Σ de TODOS os débitos deve bater com Σ de TODOS os créditos.
func SomaGlobal(ctx context.Context, q Querier) (debitos, creditos int64, err error) {
	err = q.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_cents) FILTER (WHERE direction='D'),0),
		       COALESCE(SUM(amount_cents) FILTER (WHERE direction='C'),0)
		  FROM entries`).Scan(&debitos, &creditos)
	return
}

// E2EDuplicados: mesmo E2E ID com mesmo tipo mais de uma vez.
// O índice único impede; a fitness function PROVA que impede.
func E2EDuplicados(ctx context.Context, q Querier) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT e2e_id, kind FROM transactions
			 WHERE e2e_id IS NOT NULL
			 GROUP BY e2e_id, kind HAVING count(*) > 1
		) x`).Scan(&n)
	return n, err
}

// AppendOnlyAtivo tenta um UPDATE proibido dentro de uma transação que será
// sempre descartada. Se o UPDATE passar, o guardrail morreu.
func AppendOnlyAtivo(ctx context.Context, pool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}) (bool, string) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err.Error()
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `UPDATE entries SET amount_cents = amount_cents + 1 WHERE id = (SELECT min(id) FROM entries)`)
	if err == nil {
		return false, "UPDATE em entries foi ACEITO — trigger append-only ausente"
	}
	return true, "UPDATE bloqueado pelo banco: " + err.Error()
}

func Contagens(ctx context.Context, q Querier) (transacoes, lancamentos int64, err error) {
	err = q.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM transactions), (SELECT count(*) FROM entries)`).
		Scan(&transacoes, &lancamentos)
	return
}
