package pix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/wheslleyrimar/techpix/internal/modules/bacen"
	"github.com/wheslleyrimar/techpix/internal/modules/idempotency"
	"github.com/wheslleyrimar/techpix/internal/modules/ledger"
	"github.com/wheslleyrimar/techpix/internal/modules/pix/internal/risco"
	"github.com/wheslleyrimar/techpix/internal/modules/pix/internal/store"
	"github.com/wheslleyrimar/techpix/internal/platform/httpx"
	"github.com/wheslleyrimar/techpix/internal/platform/ids"
	"github.com/wheslleyrimar/techpix/internal/platform/money"
	"github.com/wheslleyrimar/techpix/internal/platform/obs"
	"github.com/wheslleyrimar/techpix/internal/platform/pg"
)

// Contas transitórias do fluxo. `pix_a_liquidar` é o limbo EXPLÍCITO entre a
// reserva e a liquidação — é ele que torna a falha retomável (§4.5).
const (
	ContaPixALiquidar = "pix_a_liquidar"
	ContaReservaNoBC  = "reserva_no_bc"
)

type Module struct {
	db     *pg.DB
	ledger ledger.Service
	idem   idempotency.Service
	dict   bacen.DICT
	spi    bacen.SPI

	politica risco.Politica
	ispb     string

	reconcIntervalo time.Duration
	reconcIdade     time.Duration
}

var _ Service = (*Module)(nil)

type Opcoes struct {
	ISPB                string
	ValorMaximoCentavos int64
	LimiteNoturnoCentavos int64
	Blocklist           []string
	ReconcIntervalo     time.Duration
	ReconcIdade         time.Duration
}

func New(db *pg.DB, l ledger.Service, i idempotency.Service, d bacen.DICT, s bacen.SPI, o Opcoes) *Module {
	return &Module{
		db: db, ledger: l, idem: i, dict: d, spi: s, ispb: o.ISPB,
		politica: risco.Politica{
			ValorMaximoCentavos:   o.ValorMaximoCentavos,
			LimiteNoturnoCentavos: o.LimiteNoturnoCentavos,
			Blocklist:             o.Blocklist,
		},
		reconcIntervalo: o.ReconcIntervalo,
		reconcIdade:     o.ReconcIdade,
	}
}

func (m *Module) Nome() string { return "pix" }

func (m *Module) Rotas(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/pix/pagamentos", m.handlePagar)
	mux.HandleFunc("GET /v1/pix/pagamentos", m.handleListar)
	mux.HandleFunc("GET /v1/pix/pagamentos/{e2e}", m.handleConsultar)
	mux.HandleFunc("POST /v1/pix/e2e", m.handleGerarE2E)
}

// ---------------------------------------------------------------------------
// Orquestração
// ---------------------------------------------------------------------------

func (m *Module) Pagar(ctx context.Context, cmd ComandoPagar) (*Pagamento, *Resposta, error) {
	orc := obs.NewBudget()
	resp := &Resposta{StatusHTTP: http.StatusCreated}

	if err := ids.ValidateE2EID(cmd.E2EID); err != nil {
		return nil, resp, fmt.Errorf("%w: %v", ErrE2EInvalido, err)
	}

	// --- Passo 2: DICT. Externo, síncrono, no caminho crítico, com SLA p99 ≤ 1s.
	var chave *bacen.ChavePix
	err := orc.Track("2_dict_consulta", func() error {
		c, err := m.dict.Consultar(cmd.ChaveRecebedor)
		chave = c
		return err
	})
	if err != nil {
		resp.OrcamentoLatencia = orc.JSON()
		return nil, resp, err
	}

	// --- Passo 3: validações locais. Maior fatia CONTROLÁVEL do orçamento.
	//
	// Repare que os passos 2 e 3 ficam FORA da chave de idempotência. A chave
	// protege o que move dinheiro; uma recusa que acontece antes disso não
	// deixou rastro nenhum e pode ser reavaliada à vontade — inclusive porque
	// a lista restritiva de hoje pode não ser a de amanhã.
	err = orc.Track("3_validacoes_risco", func() error {
		return m.politica.Avaliar(risco.Pedido{
			ValorCentavos:    cmd.ValorCentavos,
			ContaPagador:     cmd.ContaPagador,
			ChaveRecebedor:   cmd.ChaveRecebedor,
			CPFCNPJRecebedor: chave.CPFCNPJ,
			Agora:            time.Now(),
		})
	})
	if err != nil {
		resp.OrcamentoLatencia = orc.JSON()
		return nil, resp, err
	}

	// --- Passo 4: RESERVA no ledger, protegida por idempotência.
	// É aqui que os três toques da Ana viram um débito só.
	var resIdem *idempotency.Resultado
	err = orc.Track("4_ledger_reserva", func() error {
		r, err := m.idem.Executar(ctx, cmd.E2EID, "pix.pagamento", cmd,
			func(ctx context.Context, tx pg.Tx) (int, any, error) {
				p, err := m.reservar(ctx, tx, cmd, chave)
				if err != nil {
					return http.StatusUnprocessableEntity, nil, err
				}
				return http.StatusCreated, p, nil
			})
		resIdem = r
		return err
	})

	if err != nil {
		resp.OrcamentoLatencia = orc.JSON()
		// Retry de uma recusa já registrada: devolvemos a MESMA resposta de
		// antes, sem reprocessar. Idempotência vale para o "não" também.
		var recusa *idempotency.ErroRecusaRegistrada
		if errors.As(err, &recusa) {
			obs.Inc("pix.replay_de_recusa")
			resp.Replay = true
			resp.StatusHTTP = recusa.Status
			resp.CorpoBruto = recusa.Corpo
		}
		return nil, resp, err
	}

	if resIdem.Replay {
		// Retry TARDIO ou CONCORRENTE: nenhum efeito novo. Devolvemos o estado
		// atual do MESMO pagamento. "Tocou 3x" -> "aconteceu 1x, respondido 3x".
		obs.Inc("pix.replay")
		resp.Replay = true
		resp.StatusHTTP = http.StatusOK
		resp.Observacao = "requisicao repetida com o mesmo EndToEndId: nenhum novo debito foi criado"
		resp.OrcamentoLatencia = orc.JSON()
		p, err := m.Consultar(ctx, cmd.E2EID)
		return p, resp, err
	}

	// --- Passos 5 a 7: SPI.
	// ATENÇÃO: fora de qualquer transação de banco. Segurar uma transação
	// aberta durante uma chamada de rede de segundos é como se esgota um pool
	// e se derruba a fintech inteira (Lei de Little, §3.5).
	var pacs *bacen.Pacs002
	err = orc.Track("5_spi_liquidacao", func() error {
		r, err := m.spi.Enviar(bacen.Pacs008{
			E2EID:          cmd.E2EID,
			ISPBPagador:    m.ispb,
			ISPBRecebedor:  chave.ISPB,
			ChaveRecebedor: cmd.ChaveRecebedor,
			ValorCentavos:  cmd.ValorCentavos,
			Descricao:      cmd.Descricao,
		})
		pacs = r
		return err
	})

	if err != nil {
		// Timeout do SPI = desfecho DESCONHECIDO. Não estornamos (podia ter
		// liquidado) nem reenviamos (podia duplicar). O pagamento fica em
		// RESERVED, explicitamente, e a reconciliação por E2E ID resolve.
		obs.Inc("pix.spi_indefinido")
		slog.Warn("desfecho do SPI desconhecido; pagamento fica reservado para reconciliacao",
			"e2e_id", cmd.E2EID, "erro", err)
		resp.StatusHTTP = http.StatusAccepted
		resp.Observacao = "o SPI nao respondeu a tempo: o desfecho e desconhecido. " +
			"O pagamento permanece RESERVED e a reconciliacao por E2E ID vai concluir. " +
			"Nao estornamos por conta propria: poderia destruir dinheiro ja liquidado."
		resp.OrcamentoLatencia = orc.JSON()
		p, cErr := m.Consultar(ctx, cmd.E2EID)
		return p, resp, cErr
	}

	// --- Passo 8: reconciliação contábil interna.
	err = orc.Track("6_ledger_liquidacao", func() error { return m.finalizar(ctx, cmd.E2EID, pacs) })
	if err != nil {
		slog.Error("falha ao finalizar pagamento no ledger", "e2e_id", cmd.E2EID, "erro", err)
	}

	resp.OrcamentoLatencia = orc.JSON()
	p, err := m.Consultar(ctx, cmd.E2EID)
	return p, resp, err
}

// reservar é o Fato 1 da §3.4: DÉBITO carteira / CRÉDITO pix_a_liquidar.
// Roda dentro da transação da idempotência — reserva e registro são atômicos.
func (m *Module) reservar(ctx context.Context, tx pg.Tx, cmd ComandoPagar, chave *bacen.ChavePix) (*Pagamento, error) {
	contaID, err := store.ContaIDPorCodigo(ctx, tx, cmd.ContaPagador)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ledger.ErrContaDesconhecida, cmd.ContaPagador)
		}
		return nil, err
	}

	t, err := m.ledger.RegistrarTx(ctx, tx, ledger.PedidoTransacao{
		E2EID:     cmd.E2EID,
		Tipo:      "pix_reserva",
		Descricao: fmt.Sprintf("Pix para %s (%s)", chave.Titular, cmd.ChaveRecebedor),
		Lancamentos: []ledger.Lancamento{
			{Conta: cmd.ContaPagador, Direcao: ledger.Debito, Valor: money.Cents(cmd.ValorCentavos)},
			{Conta: ContaPixALiquidar, Direcao: ledger.Credito, Valor: money.Cents(cmd.ValorCentavos)},
		},
	})
	if err != nil {
		return nil, err
	}

	p := Pagamento{
		E2EID: cmd.E2EID, ContaPagador: cmd.ContaPagador, ChaveRecebedor: cmd.ChaveRecebedor,
		ISPBRecebedor: chave.ISPB, BancoRecebedor: chave.Instituicao, NomeRecebedor: chave.Titular,
		ValorCentavos: cmd.ValorCentavos, Valor: money.Cents(cmd.ValorCentavos).BRL(),
		Descricao: cmd.Descricao, Status: Reservado, TxReserva: t.ID,
	}

	if err := store.Criar(ctx, tx, store.Pagamento{
		E2EID: p.E2EID, ContaPagador: p.ContaPagador, ChaveRecebedor: p.ChaveRecebedor,
		ISPBRecebedor: p.ISPBRecebedor, BancoRecebedor: p.BancoRecebedor, NomeRecebedor: p.NomeRecebedor,
		ValorCentavos: p.ValorCentavos, Descricao: p.Descricao, Status: string(Reservado), TxReserva: t.ID,
	}, contaID, nil); err != nil {
		return nil, err
	}

	obs.Inc("pix.reservado")
	return &p, nil
}

// finalizar é o Fato 2 da §3.4 (ou o estorno, se o SPI rejeitou).
//
//	liquidado -> DÉBITO pix_a_liquidar / CRÉDITO reserva_no_bc
//	rejeitado -> DÉBITO pix_a_liquidar / CRÉDITO carteira do pagador
//
// Note que "desfazer" NÃO é apagar a reserva. É uma NOVA transação, como manda
// a irreversibilidade do dinheiro. O log jamais é reescrito.
func (m *Module) finalizar(ctx context.Context, e2e string, pacs *bacen.Pacs002) error {
	return m.db.InSerializableTx(ctx, func(tx pg.Tx) error {
		p, err := store.Buscar(ctx, tx, e2e)
		if err != nil {
			return err
		}
		if p.Status != string(Reservado) {
			return nil // já finalizado: reconciliação e resposta do SPI se cruzaram
		}

		if pacs.Status == bacen.StatusLiquidado {
			t, err := m.ledger.RegistrarTx(ctx, tx, ledger.PedidoTransacao{
				E2EID:     e2e,
				Tipo:      "pix_liquidacao",
				Descricao: "Liquidacao no SPI em moeda de banco central",
				Lancamentos: []ledger.Lancamento{
					{Conta: ContaPixALiquidar, Direcao: ledger.Debito, Valor: money.Cents(p.ValorCentavos)},
					{Conta: ContaReservaNoBC, Direcao: ledger.Credito, Valor: money.Cents(p.ValorCentavos)},
				},
			})
			if err != nil {
				if errors.Is(err, ledger.ErrDuplicada) {
					return nil // outra rotina já liquidou; o índice único garantiu
				}
				return err
			}
			obs.Inc("pix.liquidado")
			return store.MarcarLiquidado(ctx, tx, e2e, t.ID, pacs.Status)
		}

		t, err := m.ledger.RegistrarTx(ctx, tx, ledger.PedidoTransacao{
			E2EID:     e2e,
			Tipo:      "pix_estorno",
			Descricao: "Devolucao da reserva: " + pacs.Motivo,
			Lancamentos: []ledger.Lancamento{
				{Conta: ContaPixALiquidar, Direcao: ledger.Debito, Valor: money.Cents(p.ValorCentavos)},
				{Conta: p.ContaPagador, Direcao: ledger.Credito, Valor: money.Cents(p.ValorCentavos)},
			},
		})
		if err != nil {
			if errors.Is(err, ledger.ErrDuplicada) {
				return nil
			}
			return err
		}
		obs.Inc("pix.estornado")
		return store.MarcarEstornado(ctx, tx, e2e, t.ID, pacs.Status, pacs.Motivo)
	})
}

// ---------------------------------------------------------------------------
// Reconciliação (§4.5): a resposta se perdeu, mas a verdade existe no SPI.
// ---------------------------------------------------------------------------

func (m *Module) Iniciar(ctx context.Context) {
	go func() {
		t := time.NewTicker(m.reconcIntervalo)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.reconciliar(ctx)
			}
		}
	}()
}

func (m *Module) reconciliar(ctx context.Context) {
	pendentes, err := store.Pendentes(ctx, m.db.Pool, m.reconcIdade, 50)
	if err != nil {
		slog.Error("reconciliacao: falha ao listar pendentes", "erro", err)
		return
	}

	for _, p := range pendentes {
		obs.Inc("reconciliacao.tentativa")
		pacs, err := m.spi.ConsultarStatus(p.E2EID)

		switch {
		case errors.Is(err, bacen.ErrSPINaoEncontrado):
			// O SPI nunca recebeu a instrução: o dinheiro NÃO saiu do sistema.
			// Devolver a reserva é seguro — e é a única leitura correta aqui.
			slog.Warn("reconciliacao: SPI desconhece o E2E ID; devolvendo a reserva", "e2e_id", p.E2EID)
			if err := m.finalizar(ctx, p.E2EID, &bacen.Pacs002{
				E2EID: p.E2EID, Status: bacen.StatusRejeitado, Motivo: "SPI nao registrou a instrucao",
			}); err != nil {
				slog.Error("reconciliacao: falha ao estornar", "e2e_id", p.E2EID, "erro", err)
			}
			obs.Inc("reconciliacao.estornado")

		case err != nil:
			slog.Warn("reconciliacao: SPI ainda inacessivel", "e2e_id", p.E2EID, "erro", err)
			obs.Inc("reconciliacao.adiada")

		case pacs.Status == bacen.StatusEmAnalise:
			obs.Inc("reconciliacao.em_analise")

		default:
			slog.Info("reconciliacao: desfecho recuperado no SPI",
				"e2e_id", p.E2EID, "status", pacs.Status)
			if err := m.finalizar(ctx, p.E2EID, pacs); err != nil {
				slog.Error("reconciliacao: falha ao finalizar", "e2e_id", p.E2EID, "erro", err)
				continue
			}
			obs.Inc("reconciliacao.resolvida")
		}
	}
}

// ---------------------------------------------------------------------------
// Consultas
// ---------------------------------------------------------------------------

func (m *Module) Consultar(ctx context.Context, e2eID string) (*Pagamento, error) {
	p, err := store.Buscar(ctx, m.db.Pool, e2eID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPagamentoNaoExiste
		}
		return nil, err
	}
	return converter(*p), nil
}

func (m *Module) Listar(ctx context.Context, limite int) ([]Pagamento, error) {
	if limite <= 0 || limite > 200 {
		limite = 20
	}
	ps, err := store.Listar(ctx, m.db.Pool, limite)
	if err != nil {
		return nil, err
	}
	out := make([]Pagamento, 0, len(ps))
	for _, p := range ps {
		out = append(out, *converter(p))
	}
	return out, nil
}

func converter(p store.Pagamento) *Pagamento {
	return &Pagamento{
		E2EID: p.E2EID, ContaPagador: p.ContaPagador, ChaveRecebedor: p.ChaveRecebedor,
		ISPBRecebedor: p.ISPBRecebedor, BancoRecebedor: p.BancoRecebedor, NomeRecebedor: p.NomeRecebedor,
		ValorCentavos: p.ValorCentavos, Valor: money.Cents(p.ValorCentavos).BRL(), Descricao: p.Descricao,
		Status: Status(p.Status), StatusSPI: p.StatusSPI, MotivoSPI: p.MotivoSPI,
		TxReserva: p.TxReserva, TxLiquidacao: p.TxLiquidacao,
		CriadoEm: p.CriadoEm, AtualizadoEm: p.AtualizadoEm,
	}
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

type pedidoPagar struct {
	E2EID          string `json:"e2e_id"`
	ContaPagador   string `json:"conta_pagador"`
	ChaveRecebedor string `json:"chave_recebedor"`
	ValorCentavos  int64  `json:"valor_centavos"`
	Descricao      string `json:"descricao"`
}

func (m *Module) handlePagar(w http.ResponseWriter, r *http.Request) {
	var req pedidoPagar
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}

	// A chave também pode vir no cabeçalho, como manda a convenção de APIs.
	if req.E2EID == "" {
		req.E2EID = r.Header.Get("Idempotency-Key")
	}
	if req.E2EID == "" {
		httpx.Fail(w, http.StatusBadRequest, "E2E_OBRIGATORIO",
			"o EndToEndId precisa nascer no cliente e sobreviver aos retries. "+
				"Gere um em POST /v1/pix/e2e e reenvie o MESMO valor em todas as tentativas.", nil)
		return
	}

	pag, resp, err := m.Pagar(r.Context(), ComandoPagar{
		E2EID: req.E2EID, ContaPagador: req.ContaPagador, ChaveRecebedor: req.ChaveRecebedor,
		ValorCentavos: req.ValorCentavos, Descricao: req.Descricao,
	})

	if err != nil {
		m.responderErro(w, resp, err)
		return
	}

	if resp.Replay {
		w.Header().Set("X-Idempotent-Replay", "true")
	}
	httpx.JSON(w, resp.StatusHTTP, map[string]any{
		"pagamento":         pag,
		"replay":            resp.Replay,
		"observacao":        resp.Observacao,
		"orcamento_latencia": resp.OrcamentoLatencia,
	})
}

// responderErro traduz cada falha para o código HTTP que o cliente precisa
// para decidir se pode ou não tentar de novo. Na dúvida, falhamos FECHADO:
// nenhuma resposta ambígua sugere que o dinheiro se moveu.
func (m *Module) responderErro(w http.ResponseWriter, resp *Resposta, err error) {
	detalhes := map[string]any{}
	if resp != nil && resp.OrcamentoLatencia != nil {
		detalhes["orcamento_latencia"] = resp.OrcamentoLatencia
	}

	// Replay de recusa: a resposta guardada volta byte a byte.
	if resp != nil && len(resp.CorpoBruto) > 0 {
		w.Header().Set("X-Idempotent-Replay", "true")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		status := resp.StatusHTTP
		if status == 0 {
			status = http.StatusUnprocessableEntity
		}
		w.WriteHeader(status)
		_, _ = w.Write(resp.CorpoBruto)
		return
	}

	var motivo *risco.Motivo
	var erroSaldo *ledger.ErroSaldo

	switch {
	case errors.Is(err, ErrE2EInvalido):
		httpx.Fail(w, http.StatusBadRequest, "E2E_INVALIDO", err.Error(), detalhes)
	case errors.Is(err, bacen.ErrChaveInvalida):
		httpx.Fail(w, http.StatusBadRequest, "CHAVE_INVALIDA",
			err.Error()+" — bloqueada localmente, sem gastar token do DICT", detalhes)
	case errors.Is(err, bacen.ErrChaveNaoEncontrada):
		httpx.Fail(w, http.StatusNotFound, "CHAVE_NAO_ENCONTRADA",
			"chave nao existe no DICT (esse 404 custou 20 tokens do balde)", detalhes)
	case errors.Is(err, bacen.ErrDictRateLimited):
		httpx.Fail(w, http.StatusTooManyRequests, "DICT_RATE_LIMITED",
			"balde de tokens do DICT esgotado; reduza consultas ou melhore o cache", detalhes)
	case errors.Is(err, bacen.ErrDictTimeout), errors.Is(err, bacen.ErrDictIndisponivel):
		httpx.Fail(w, http.StatusServiceUnavailable, "DICT_INDISPONIVEL",
			err.Error()+" — nenhum valor foi movimentado (falha fechada)", detalhes)
	case errors.As(err, &motivo):
		detalhes["motivo"] = motivo.Codigo
		httpx.Fail(w, http.StatusUnprocessableEntity, "RECUSADO_POR_RISCO", motivo.Mensagem, detalhes)
	case errors.As(err, &erroSaldo):
		detalhes["conta"] = erroSaldo.Conta
		detalhes["saldo"] = erroSaldo.Saldo.BRL()
		httpx.Fail(w, http.StatusUnprocessableEntity, "SALDO_INSUFICIENTE", erroSaldo.Error(), detalhes)
	case errors.Is(err, ledger.ErrSaldoInsuficiente):
		httpx.Fail(w, http.StatusUnprocessableEntity, "SALDO_INSUFICIENTE", err.Error(), detalhes)
	case errors.Is(err, ledger.ErrContaDesconhecida):
		httpx.Fail(w, http.StatusNotFound, "CONTA_DESCONHECIDA", err.Error(), detalhes)
	case errors.Is(err, ledger.ErrDuplicada):
		httpx.Fail(w, http.StatusConflict, "E2E_DUPLICADO",
			"ja existe transacao com este EndToEndId (regra do BACEN: E2E ID e unico)", detalhes)
	case errors.Is(err, idempotency.ErrEmAndamento):
		httpx.Fail(w, http.StatusConflict, "EM_ANDAMENTO",
			"uma tentativa com esta mesma chave ainda esta em andamento; "+
				"repita a consulta em instantes com o mesmo EndToEndId", detalhes)
	case errors.Is(err, idempotency.ErrChaveReutilizada):
		httpx.Fail(w, http.StatusUnprocessableEntity, "CHAVE_REUTILIZADA",
			"este EndToEndId ja foi usado com outro payload; a chave identifica a INTENCAO", detalhes)
	default:
		slog.Error("falha nao mapeada no pagamento", "erro", err)
		httpx.Fail(w, http.StatusInternalServerError, "ERRO_INTERNO",
			"falha interna; nenhuma liquidacao foi confirmada", detalhes)
	}
}

func (m *Module) handleConsultar(w http.ResponseWriter, r *http.Request) {
	p, err := m.Consultar(r.Context(), r.PathValue("e2e"))
	if err != nil {
		httpx.Fail(w, http.StatusNotFound, "PAGAMENTO_NAO_ENCONTRADO", err.Error(), nil)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (m *Module) handleListar(w http.ResponseWriter, r *http.Request) {
	limite, _ := strconv.Atoi(r.URL.Query().Get("limite"))
	ps, err := m.Listar(r.Context(), limite)
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "ERRO_INTERNO", err.Error(), nil)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"pagamentos": ps})
}

// handleGerarE2E existe só para a demo: em produção, quem gera é o APP do
// pagador. A chave nasce no cliente — se nascesse aqui, cada retry teria uma
// chave nova e a deduplicação seria uma ilusão.
func (m *Module) handleGerarE2E(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"e2e_id": ids.NewE2EID(m.ispb, time.Now()),
		"aviso":  "em producao este identificador nasce no app do pagador e sobrevive a todos os retries",
	})
}
