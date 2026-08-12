// Package accounts cuida das contas de cliente e das chaves Pix DESTE
// participante.
//
// O módulo não sabe somar saldo nem gravar lançamento: para isso ele pede ao
// ledger. Fronteira de módulo é isso — não é pasta, é quem pode fazer o quê.
package accounts

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/wheslleyrimar/techpix/internal/modules/idempotency"
	"github.com/wheslleyrimar/techpix/internal/modules/ledger"
	"github.com/wheslleyrimar/techpix/internal/platform/httpx"
	"github.com/wheslleyrimar/techpix/internal/platform/money"
	"github.com/wheslleyrimar/techpix/internal/platform/pg"
)

// ContaReservaNoBC é a contrapartida de todo cash-in: o dinheiro que entra
// aparece como ATIVO na nossa Conta PI e como PASSIVO na carteira do cliente.
const ContaReservaNoBC = "reserva_no_bc"

type Module struct {
	db     *pg.DB
	ledger ledger.Service
	idem   idempotency.Service
}

func New(db *pg.DB, l ledger.Service, i idempotency.Service) *Module {
	return &Module{db: db, ledger: l, idem: i}
}

func (m *Module) Nome() string { return "accounts" }

func (m *Module) Rotas(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/contas", m.handleListar)
	mux.HandleFunc("POST /v1/contas/{codigo}/depositos", m.handleDepositar)
}

type Cliente struct {
	Conta   string      `json:"conta"`
	Titular string      `json:"titular"`
	CPF     string      `json:"cpf"`
	Chaves  []string    `json:"chaves_pix"`
	Saldo   string      `json:"saldo"`
	SaldoCentavos money.Cents `json:"saldo_centavos"`
}

func (m *Module) Clientes(ctx context.Context) ([]Cliente, error) {
	rows, err := m.db.Pool.Query(ctx, `
		SELECT a.code, COALESCE(a.owner_name,''), COALESCE(a.owner_tax_id,''),
		       COALESCE(array_agg(k.key) FILTER (WHERE k.key IS NOT NULL), '{}'),
		       COALESCE(account_balance(a.id), 0)
		  FROM accounts a
		  LEFT JOIN pix_keys k ON k.account_id = a.id
		 WHERE a.code LIKE 'carteira:%'
		 GROUP BY a.id, a.code, a.owner_name, a.owner_tax_id
		 ORDER BY a.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Cliente
	for rows.Next() {
		var c Cliente
		var saldo int64
		if err := rows.Scan(&c.Conta, &c.Titular, &c.CPF, &c.Chaves, &saldo); err != nil {
			return nil, err
		}
		c.SaldoCentavos = money.Cents(saldo)
		c.Saldo = c.SaldoCentavos.BRL()
		out = append(out, c)
	}
	return out, rows.Err()
}

type pedidoDeposito struct {
	ValorCentavos int64  `json:"valor_centavos"`
	Descricao     string `json:"descricao"`
}

// handleDepositar existe para preparar o cenário da aula (dar saldo à Ana).
// Mesmo aqui o dinheiro entra por partida dobrada e com idempotência — não há
// atalho "UPDATE saldo" em lugar nenhum deste sistema.
func (m *Module) handleDepositar(w http.ResponseWriter, r *http.Request) {
	var req pedidoDeposito
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	codigo := r.PathValue("codigo")
	if req.ValorCentavos <= 0 {
		httpx.Fail(w, http.StatusBadRequest, "VALOR_INVALIDO", "valor deve ser positivo", nil)
		return
	}

	chave := r.Header.Get("Idempotency-Key")
	if chave == "" {
		httpx.Fail(w, http.StatusBadRequest, "CHAVE_OBRIGATORIA",
			"envie o cabecalho Idempotency-Key: todo movimento de dinheiro nasce de uma intencao identificada", nil)
		return
	}

	res, err := m.idem.Executar(r.Context(), chave, "conta.deposito", req,
		func(ctx context.Context, tx pg.Tx) (int, any, error) {
			t, err := m.ledger.RegistrarTx(ctx, tx, ledger.PedidoTransacao{
				Tipo:      "deposito",
				Descricao: strings.TrimSpace("Deposito em " + codigo + " " + req.Descricao),
				Lancamentos: []ledger.Lancamento{
					{Conta: ContaReservaNoBC, Direcao: ledger.Debito, Valor: money.Cents(req.ValorCentavos)},
					{Conta: codigo, Direcao: ledger.Credito, Valor: money.Cents(req.ValorCentavos)},
				},
			})
			if err != nil {
				return http.StatusUnprocessableEntity, nil, err
			}
			return http.StatusCreated, t, nil
		})

	if err != nil {
		var recusa *idempotency.ErroRecusaRegistrada
		if errors.As(err, &recusa) {
			// Mesma chave, mesma recusa: a resposta original volta intacta.
			w.Header().Set("X-Idempotent-Replay", "true")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(recusa.Status)
			_, _ = w.Write(recusa.Corpo)
			return
		}
		switch {
		case errors.Is(err, ledger.ErrContaDesconhecida):
			httpx.Fail(w, http.StatusNotFound, "CONTA_DESCONHECIDA", err.Error(), nil)
		case errors.Is(err, idempotency.ErrEmAndamento):
			httpx.Fail(w, http.StatusConflict, "EM_ANDAMENTO", err.Error(), nil)
		case errors.Is(err, idempotency.ErrChaveReutilizada):
			httpx.Fail(w, http.StatusUnprocessableEntity, "CHAVE_REUTILIZADA", err.Error(), nil)
		default:
			httpx.Fail(w, http.StatusUnprocessableEntity, "DEPOSITO_RECUSADO", err.Error(), nil)
		}
		return
	}

	if res.Replay {
		w.Header().Set("X-Idempotent-Replay", "true")
	}
	saldo, _ := m.ledger.SaldoForte(r.Context(), codigo)
	httpx.JSON(w, res.Status, map[string]any{
		"replay":         res.Replay,
		"conta":          codigo,
		"saldo_forte":    saldo.BRL(),
		"transacao_json": string(res.Corpo),
	})
}

func (m *Module) handleListar(w http.ResponseWriter, r *http.Request) {
	cs, err := m.Clientes(r.Context())
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "ERRO_INTERNO", err.Error(), nil)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"clientes": cs})
}
