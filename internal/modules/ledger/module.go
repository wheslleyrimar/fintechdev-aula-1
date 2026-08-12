package ledger

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/wheslleyrimar/techpix/internal/modules/ledger/internal/store"
	"github.com/wheslleyrimar/techpix/internal/platform/httpx"
	"github.com/wheslleyrimar/techpix/internal/platform/ids"
	"github.com/wheslleyrimar/techpix/internal/platform/money"
	"github.com/wheslleyrimar/techpix/internal/platform/obs"
	"github.com/wheslleyrimar/techpix/internal/platform/pg"
)

type Module struct {
	db         *pg.DB
	pessimista bool
}

var _ Service = (*Module)(nil)

// New monta o módulo. `lockMode` escolhe entre controle otimista (SSI) e
// pessimista (SELECT ... FOR UPDATE) — §3.7.
func New(db *pg.DB, lockMode string) *Module {
	return &Module{db: db, pessimista: strings.EqualFold(lockMode, "pessimistic")}
}

func (m *Module) Nome() string { return "ledger" }

func (m *Module) Rotas(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/ledger/contas", m.handleContas)
	mux.HandleFunc("GET /v1/ledger/contas/{codigo}", m.handleConta)
	mux.HandleFunc("GET /v1/ledger/transacoes", m.handleListarTransacoes)
	mux.HandleFunc("GET /v1/ledger/transacoes/{id}", m.handleTransacao)
	mux.HandleFunc("GET /v1/ledger/e2e/{e2e}", m.handlePorE2E)
	// Sandbox: tentativas de violar as invariantes, para a turma VER o
	// guardrail reagindo. Nenhuma delas persiste — todas terminam em rollback.
	mux.HandleFunc("POST /v1/ledger/tentativas", m.handleTentativa)
	// Harness (§7.4): invariante como endpoint. Roda em produção, ao vivo.
	mux.HandleFunc("GET /v1/fitness", m.handleFitness)
}

func (m *Module) Check(ctx context.Context) error {
	return m.db.Pool.Ping(ctx)
}

// ---------------------------------------------------------------------------
// Escrita — a única porta pela qual dinheiro se move
// ---------------------------------------------------------------------------

func (m *Module) Registrar(ctx context.Context, p PedidoTransacao) (*Transacao, error) {
	var out *Transacao
	err := m.db.InSerializableTx(ctx, func(tx pg.Tx) error {
		t, err := m.RegistrarTx(ctx, tx, p)
		if err != nil {
			return err
		}
		out = t
		return nil
	})
	return out, err
}

// RegistrarTx grava um fato econômico dentro da transação do chamador.
//
// A ordem importa e cada passo é uma regra da Aula 1:
//  1. validar a partida dobrada ANTES de tocar no banco (barato, explícito)
//  2. resolver as contas (com ou sem lock, conforme a estratégia escolhida)
//  3. gravar transação + lançamentos (append-only)
//  4. verificar saldo DEPOIS de gravar, ainda dentro da transação:
//     sob SERIALIZABLE isso é seguro, e é o que impede o lost update clássico
//     (dois pagamentos concorrentes que, somados, estouram o saldo).
func (m *Module) RegistrarTx(ctx context.Context, tx pg.Tx, p PedidoTransacao) (*Transacao, error) {
	if err := validarPedido(p); err != nil {
		return nil, err
	}

	codigos := make([]string, 0, len(p.Lancamentos))
	vistos := map[string]bool{}
	for _, l := range p.Lancamentos {
		if !vistos[l.Conta] {
			vistos[l.Conta] = true
			codigos = append(codigos, l.Conta)
		}
	}

	contas, err := store.CarregarContas(ctx, tx, codigos, m.pessimista)
	if err != nil {
		return nil, err
	}
	for _, c := range codigos {
		if _, ok := contas[c]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrContaDesconhecida, c)
		}
	}

	txID := ids.NewUUID()
	if err := store.InserirTransacao(ctx, tx, txID, p.E2EID, p.Tipo, p.Descricao); err != nil {
		if pg.IsUniqueViolation(err) {
			// Rede duplicou a mensagem. O banco recusa o segundo efeito.
			obs.Inc("ledger.e2e_duplicado_bloqueado")
			return nil, fmt.Errorf("%w: %s/%s", ErrDuplicada, p.E2EID, p.Tipo)
		}
		return nil, err
	}

	out := &Transacao{ID: txID, E2EID: p.E2EID, Tipo: p.Tipo, Descricao: p.Descricao}
	for _, l := range p.Lancamentos {
		c := contas[l.Conta]
		id, err := store.InserirLancamento(ctx, tx, txID, c.ID, string(l.Direcao), l.Valor)
		if err != nil {
			return nil, err
		}
		out.Lancamentos = append(out.Lancamentos, LancamentoView{
			ID: id, Conta: l.Conta, Direcao: l.Direcao, Valor: l.Valor, ValorBR: l.Valor.BRL(),
		})
	}

	// Conservação: nenhuma conta pode terminar negativa (exceto as que podem).
	for _, codigo := range codigos {
		c := contas[codigo]
		if c.PermiteNeg {
			continue
		}
		saldo, err := store.SaldoPorID(ctx, tx, c.ID)
		if err != nil {
			return nil, err
		}
		if saldo < 0 {
			obs.Inc("ledger.saldo_insuficiente")
			return nil, &ErroSaldo{Conta: codigo, Saldo: saldo + valorLiquido(p, codigo), Necessario: -saldo}
		}
	}

	obs.Inc("ledger.transacao_registrada")
	return out, nil
}

// valorLiquido reconstrói o efeito desta transação numa conta, só para a
// mensagem de erro ficar legível ("saldo era X, precisava de Y").
func valorLiquido(p PedidoTransacao, codigo string) money.Cents {
	var v money.Cents
	for _, l := range p.Lancamentos {
		if l.Conta != codigo {
			continue
		}
		if l.Direcao == Credito {
			v += l.Valor
		} else {
			v -= l.Valor
		}
	}
	return -v
}

func validarPedido(p PedidoTransacao) error {
	if p.Tipo == "" {
		return fmt.Errorf("%w: tipo obrigatorio", ErrPedidoInvalido)
	}
	if len(p.Lancamentos) < 2 {
		return fmt.Errorf("%w: um fato economico precisa de pelo menos 2 lancamentos", ErrPedidoInvalido)
	}

	var debitos, creditos money.Cents
	for _, l := range p.Lancamentos {
		if l.Valor <= 0 {
			return fmt.Errorf("%w: valor deve ser positivo (%s)", ErrPedidoInvalido, l.Conta)
		}
		switch l.Direcao {
		case Debito:
			debitos += l.Valor
		case Credito:
			creditos += l.Valor
		default:
			return fmt.Errorf("%w: direcao deve ser D ou C", ErrPedidoInvalido)
		}
	}

	// FITNESS FUNCTION #1, no código, antes do banco.
	if debitos != creditos {
		obs.Inc("ledger.desbalanceada_bloqueada")
		return fmt.Errorf("%w: debitos=%s creditos=%s", ErrDesbalanceada, debitos.BRL(), creditos.BRL())
	}
	return nil
}

// ---------------------------------------------------------------------------
// Leitura forte (dentro do núcleo)
// ---------------------------------------------------------------------------

func (m *Module) SaldoForte(ctx context.Context, conta string) (money.Cents, error) {
	_, saldo, err := store.SaldoPorCodigo(ctx, m.db.Pool, conta)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: %s", ErrContaDesconhecida, conta)
	}
	return saldo, err
}

func (m *Module) SaldoForteTx(ctx context.Context, tx pg.Tx, conta string) (money.Cents, error) {
	_, saldo, err := store.SaldoPorCodigo(ctx, tx, conta)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: %s", ErrContaDesconhecida, conta)
	}
	return saldo, err
}

func (m *Module) Conta(ctx context.Context, codigo string) (*Conta, error) {
	contas, err := m.Contas(ctx)
	if err != nil {
		return nil, err
	}
	for i := range contas {
		if contas[i].Codigo == codigo {
			return &contas[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrContaDesconhecida, codigo)
}

func (m *Module) Contas(ctx context.Context) ([]Conta, error) {
	cs, saldos, err := store.ListarContas(ctx, m.db.Pool)
	if err != nil {
		return nil, err
	}
	out := make([]Conta, 0, len(cs))
	for i, c := range cs {
		out = append(out, Conta{
			ID: c.ID, Codigo: c.Codigo, Nome: c.Nome, Natureza: c.Natureza,
			PermiteNeg: c.PermiteNeg, Titular: c.Titular,
			SaldoCentavos: saldos[i], Saldo: saldos[i].BRL(),
		})
	}
	return out, nil
}

func (m *Module) Transacao(ctx context.Context, id string) (*Transacao, error) {
	ts, err := m.buscar(ctx, "t.id = $1::uuid", id)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	return &ts[0], nil
}

func (m *Module) TransacoesPorE2E(ctx context.Context, e2e string) ([]Transacao, error) {
	return m.buscar(ctx, "t.e2e_id = $1", e2e)
}

func (m *Module) buscar(ctx context.Context, where string, arg any) ([]Transacao, error) {
	ts, err := store.BuscarTransacoes(ctx, m.db.Pool, where, arg)
	if err != nil {
		return nil, err
	}
	out := make([]Transacao, 0, len(ts))
	for _, t := range ts {
		tv := Transacao{ID: t.ID, E2EID: t.E2EID, Tipo: t.Tipo, Descricao: t.Descricao, OcorridoEm: t.OcorridoEm}
		for _, l := range t.Lancamentos {
			tv.Lancamentos = append(tv.Lancamentos, LancamentoView{
				ID: l.ID, Conta: l.Conta, Direcao: Direcao(l.Direcao), Valor: l.Valor, ValorBR: l.Valor.BRL(),
			})
		}
		out = append(out, tv)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Harness: invariantes como teste, rodando contra o banco de verdade
// ---------------------------------------------------------------------------

func (m *Module) Fitness(ctx context.Context) (*Relatorio, error) {
	rel := &Relatorio{Aprovado: true, Resumo: map[string]any{}}

	add := func(nome, invariante string, ok bool, detalhe string) {
		rel.Checks = append(rel.Checks, Check{Nome: nome, Invariante: invariante, Aprovado: ok, Detalhe: detalhe})
		if !ok {
			rel.Aprovado = false
		}
	}

	desbalanceadas, err := store.TransacoesDesbalanceadas(ctx, m.db.Pool)
	if err != nil {
		return nil, err
	}
	add("partida_dobrada", "em toda transacao, Σ debitos = Σ creditos",
		desbalanceadas == 0, fmt.Sprintf("%d transacoes desbalanceadas", desbalanceadas))

	debitos, creditos, err := store.SomaGlobal(ctx, m.db.Pool)
	if err != nil {
		return nil, err
	}
	add("conservacao_global", "Σ de todos os debitos = Σ de todos os creditos",
		debitos == creditos,
		fmt.Sprintf("debitos=%s creditos=%s", money.Cents(debitos).BRL(), money.Cents(creditos).BRL()))

	negativas, err := store.ContasNegativas(ctx, m.db.Pool)
	if err != nil {
		return nil, err
	}
	add("saldo_nao_negativo", "conta sem permissao de credito nunca fica negativa",
		len(negativas) == 0, fmt.Sprintf("contas negativas: %v", negativas))

	dups, err := store.E2EDuplicados(ctx, m.db.Pool)
	if err != nil {
		return nil, err
	}
	add("e2e_unico", "EndToEndId unico por tipo de transacao (regra BACEN)",
		dups == 0, fmt.Sprintf("%d duplicatas", dups))

	appendOnly, detalhe := store.AppendOnlyAtivo(ctx, m.db.Pool)
	add("append_only", "lancamento gravado nunca e alterado nem apagado", appendOnly, detalhe)

	tcount, ecount, err := store.Contagens(ctx, m.db.Pool)
	if err != nil {
		return nil, err
	}
	rel.Resumo["transacoes"] = tcount
	rel.Resumo["lancamentos"] = ecount
	rel.Resumo["lancamentos_por_transacao"] = razao(ecount, tcount)
	rel.Resumo["modo_de_lock"] = map[bool]string{true: "pessimista (SELECT FOR UPDATE)", false: "otimista (SSI)"}[m.pessimista]

	return rel, nil
}

func razao(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

func (m *Module) handleContas(w http.ResponseWriter, r *http.Request) {
	contas, err := m.Contas(r.Context())
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "ERRO_INTERNO", err.Error(), nil)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"contas": contas,
		"nota":   "saldo aqui e SOMA sobre o log (consistencia forte), nao coluna",
	})
}

func (m *Module) handleConta(w http.ResponseWriter, r *http.Request) {
	c, err := m.Conta(r.Context(), r.PathValue("codigo"))
	if err != nil {
		httpx.Fail(w, http.StatusNotFound, "CONTA_DESCONHECIDA", err.Error(), nil)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (m *Module) handleTransacao(w http.ResponseWriter, r *http.Request) {
	t, err := m.Transacao(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, "ID_INVALIDO", err.Error(), nil)
		return
	}
	if t == nil {
		httpx.Fail(w, http.StatusNotFound, "TRANSACAO_NAO_ENCONTRADA", "transacao inexistente", nil)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}

func (m *Module) handlePorE2E(w http.ResponseWriter, r *http.Request) {
	ts, err := m.TransacoesPorE2E(r.Context(), r.PathValue("e2e"))
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "ERRO_INTERNO", err.Error(), nil)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"e2e_id":     r.PathValue("e2e"),
		"transacoes": ts,
		"nota":       "um Pix gera 2 transacoes: reserva e liquidacao. Mesmo E2E ID nas duas.",
	})
}

func (m *Module) handleListarTransacoes(w http.ResponseWriter, r *http.Request) {
	limite, _ := strconv.Atoi(r.URL.Query().Get("limite"))
	if limite <= 0 || limite > 200 {
		limite = 25
	}
	ts, err := store.ListarTransacoes(r.Context(), m.db.Pool, limite)
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "ERRO_INTERNO", err.Error(), nil)
		return
	}

	out := make([]Transacao, 0, len(ts))
	for _, t := range ts {
		tv := Transacao{ID: t.ID, E2EID: t.E2EID, Tipo: t.Tipo, Descricao: t.Descricao, OcorridoEm: t.OcorridoEm}
		for _, l := range t.Lancamentos {
			tv.Lancamentos = append(tv.Lancamentos, LancamentoView{
				ID: l.ID, Conta: l.Conta, Direcao: Direcao(l.Direcao), Valor: l.Valor, ValorBR: l.Valor.BRL(),
			})
		}
		out = append(out, tv)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"transacoes": out})
}

// ---------------------------------------------------------------------------
// Sandbox de violação: o guardrail só convence quando a turma tenta furá-lo
// ---------------------------------------------------------------------------

type PedidoTentativa struct {
	Tipo  string `json:"tipo"`
	Conta string `json:"conta"`
}

type ResultadoTentativa struct {
	Tipo        string `json:"tipo"`
	Bloqueado   bool   `json:"bloqueado"`
	Camada      string `json:"camada"`
	Erro        string `json:"erro"`
	Explicacao  string `json:"explicacao"`
	Invariante  string `json:"invariante"`
}

func (m *Module) handleTentativa(w http.ResponseWriter, r *http.Request) {
	var req PedidoTentativa
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Conta == "" {
		req.Conta = "carteira:ana"
	}

	ctx := r.Context()
	res := ResultadoTentativa{Tipo: req.Tipo}

	switch req.Tipo {
	case "desbalanceada":
		res.Invariante = "em toda transacao, Σ debitos = Σ creditos"
		res.Camada = "aplicacao — ledger.validarPedido, antes de tocar no banco"
		res.Explicacao = "debito de R$ 1,00 contra credito de R$ 9,00: R$ 8,00 sairiam do nada"
		_, err := m.Registrar(ctx, PedidoTransacao{
			Tipo:      "tentativa_desbalanceada",
			Descricao: "sandbox da aula",
			Lancamentos: []Lancamento{
				{Conta: req.Conta, Direcao: Debito, Valor: 100},
				{Conta: "pix_a_liquidar", Direcao: Credito, Valor: 900},
			},
		})
		res.Bloqueado, res.Erro = err != nil, textoErro(err)

	case "desbalanceada_no_banco":
		res.Invariante = "em toda transacao, Σ debitos = Σ creditos"
		res.Camada = "banco — constraint trigger DEFERRABLE, cobrada no COMMIT"
		res.Explicacao = "INSERT direto no banco, passando por fora da aplicacao: um credito sem contrapartida"
		res.Bloqueado, res.Erro = m.tentarCreditoSolto(ctx, req.Conta)

	case "append_only":
		res.Invariante = "lancamento gravado nunca e alterado nem apagado"
		res.Camada = "banco — trigger BEFORE UPDATE OR DELETE"
		res.Explicacao = "UPDATE em entries, como faria um acerto manual as 3h da manha"
		ok, detalhe := store.AppendOnlyAtivo(ctx, m.db.Pool)
		res.Bloqueado, res.Erro = ok, detalhe

	case "saldo_negativo":
		res.Invariante = "conta de cliente nunca fica negativa"
		res.Camada = "aplicacao dentro da transacao SERIALIZABLE"
		res.Explicacao = "debito de R$ 1.000.000,00 numa carteira que nao tem esse saldo"
		_, err := m.Registrar(ctx, PedidoTransacao{
			Tipo:      "tentativa_saldo_negativo",
			Descricao: "sandbox da aula",
			Lancamentos: []Lancamento{
				{Conta: req.Conta, Direcao: Debito, Valor: 100_000_000},
				{Conta: "pix_a_liquidar", Direcao: Credito, Valor: 100_000_000},
			},
		})
		res.Bloqueado, res.Erro = err != nil, textoErro(err)

	default:
		httpx.Fail(w, http.StatusBadRequest, "TENTATIVA_DESCONHECIDA",
			"tipo deve ser: desbalanceada, desbalanceada_no_banco, append_only ou saldo_negativo", nil)
		return
	}

	if !res.Bloqueado {
		// Se chegou aqui, um guardrail morreu. É incidente, não resultado.
		obs.Inc("harness.guardrail_falhou")
	}
	httpx.JSON(w, http.StatusOK, res)
}

// tentarCreditoSolto grava um crédito sem contrapartida e tenta commitar.
// A transação é sempre descartada: se o COMMIT falhar, o trigger funcionou;
// se passar, desfazemos na mão e reportamos o guardrail como quebrado.
func (m *Module) tentarCreditoSolto(ctx context.Context, conta string) (bool, string) {
	tx, err := m.db.Pool.Begin(ctx)
	if err != nil {
		return false, err.Error()
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txID := ids.NewUUID()
	if err := store.InserirTransacao(ctx, tx, txID, "", "tentativa_credito_solto", "sandbox da aula"); err != nil {
		return true, "bloqueado ja no INSERT da transacao: " + err.Error()
	}
	contas, err := store.CarregarContas(ctx, tx, []string{conta}, false)
	if err != nil {
		return false, err.Error()
	}
	c, ok := contas[conta]
	if !ok {
		return false, "conta desconhecida: " + conta
	}
	if _, err := store.InserirLancamento(ctx, tx, txID, c.ID, "C", 500_000); err != nil {
		return true, "bloqueado no INSERT do lancamento: " + err.Error()
	}

	// O crédito solto só é cobrado no COMMIT (constraint trigger DEFERRED).
	if err := tx.Commit(ctx); err != nil {
		return true, "bloqueado no COMMIT: " + err.Error()
	}
	return false, "o banco ACEITOU um credito sem contrapartida — a partida dobrada nao esta protegida"
}

func textoErro(err error) string {
	if err == nil {
		return "nenhum erro: a operacao passou"
	}
	return err.Error()
}

func (m *Module) handleFitness(w http.ResponseWriter, r *http.Request) {
	rel, err := m.Fitness(r.Context())
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "ERRO_INTERNO", err.Error(), nil)
		return
	}
	status := http.StatusOK
	if !rel.Aprovado {
		status = http.StatusInternalServerError // guardrail violado é incidente
	}
	httpx.JSON(w, status, rel)
}
