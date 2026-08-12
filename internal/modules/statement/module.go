package statement

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/wheslleyrimar/techpix/internal/modules/ledger"
	"github.com/wheslleyrimar/techpix/internal/modules/statement/internal/store"
	"github.com/wheslleyrimar/techpix/internal/platform/httpx"
	"github.com/wheslleyrimar/techpix/internal/platform/money"
	"github.com/wheslleyrimar/techpix/internal/platform/obs"
	"github.com/wheslleyrimar/techpix/internal/platform/pg"
)

const cursorNome = "statement"

type Module struct {
	db     *pg.DB
	ledger ledger.Service

	// atraso: defasagem PROPOSITAL da projeção. Em produção ela existe de
	// graça (fila, rede, lote). Aqui a tornamos visível para a aula.
	atraso   time.Duration
	intervalo time.Duration
}

var _ Service = (*Module)(nil)

func New(db *pg.DB, l ledger.Service, atraso, intervalo time.Duration) *Module {
	return &Module{db: db, ledger: l, atraso: atraso, intervalo: intervalo}
}

func (m *Module) Nome() string { return "statement" }

func (m *Module) Rotas(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/contas/{codigo}/saldo", m.handleSaldo)
	mux.HandleFunc("GET /v1/contas/{codigo}/extrato", m.handleExtrato)
	mux.HandleFunc("GET /v1/projecao", m.handleProjecao)
}

// ---------------------------------------------------------------------------
// Projetor: o log vira saldo e extrato. Sempre nessa direção, nunca o contrário.
// ---------------------------------------------------------------------------

func (m *Module) Iniciar(ctx context.Context) {
	go func() {
		t := time.NewTicker(m.intervalo)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := m.projetar(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.Error("projecao falhou", "erro", err)
				}
			}
		}
	}()
}

func (m *Module) projetar(ctx context.Context) error {
	cursor, err := store.Cursor(ctx, m.db.Pool, cursorNome)
	if err != nil {
		return err
	}

	lote, err := store.LancamentosApos(ctx, m.db.Pool, cursor, 500)
	if err != nil || len(lote) == 0 {
		return err
	}

	// O atraso proposital: é este sleep que faz o aluno VER a janela em que o
	// saldo forte e o saldo eventual discordam — e entender que discordar por
	// alguns milissegundos é uma escolha, não um defeito.
	if m.atraso > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.atraso):
		}
	}

	// Read committed basta: a borda não precisa de serializable.
	return m.db.InReadCommittedTx(ctx, func(tx pg.Tx) error {
		saldos := map[string]int64{}
		var ultimo int64

		for _, l := range lote {
			saldo, ok := saldos[l.ContaID]
			if !ok {
				saldo, err = store.SaldoProjetado(ctx, tx, l.ContaID)
				if err != nil {
					return err
				}
			}

			assinado := int64(l.ValorCentavos)
			if l.Direcao == "D" {
				assinado = -assinado
			}
			if l.Natureza == "ATIVO" { // ativo tem saldo devedor
				assinado = -assinado
			}
			saldo += assinado
			saldos[l.ContaID] = saldo

			if err := store.InserirExtrato(ctx, tx, l, assinado, saldo); err != nil {
				return err
			}
			ultimo = l.ID
		}

		for contaID, saldo := range saldos {
			if err := store.AplicarSaldo(ctx, tx, contaID, saldo, ultimo); err != nil {
				return err
			}
		}

		obs.Add("projecao.lancamentos", int64(len(lote)))
		return store.AvancarCursor(ctx, tx, cursorNome, ultimo)
	})
}

// ---------------------------------------------------------------------------
// Leituras eventuais
// ---------------------------------------------------------------------------

func (m *Module) SaldoEventual(ctx context.Context, conta string) (*SaldoProjetado, error) {
	p, err := store.BuscarProjecao(ctx, m.db.Pool, conta)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ledger.ErrContaDesconhecida
		}
		return nil, err
	}
	return &SaldoProjetado{
		ContaCodigo:      conta,
		SaldoCentavos:    money.Cents(p.SaldoCentavos),
		Saldo:            money.Cents(p.SaldoCentavos).BRL(),
		UltimoLancamento: p.UltimoLancamento,
		AtualizadoEm:     p.AtualizadoEm,
		AtrasoMs:         float64(time.Since(p.AtualizadoEm).Microseconds()) / 1000,
	}, nil
}

func (m *Module) Extrato(ctx context.Context, conta string, limite int) ([]LinhaExtrato, error) {
	if limite <= 0 || limite > 200 {
		limite = 20
	}
	ls, err := store.Extrato(ctx, m.db.Pool, conta, limite)
	if err != nil {
		return nil, err
	}
	out := make([]LinhaExtrato, 0, len(ls))
	for _, l := range ls {
		out = append(out, LinhaExtrato{
			LancamentoID: l.LancamentoID, E2EID: l.E2EID, Tipo: l.Tipo, Descricao: l.Descricao,
			Direcao: l.Direcao, ValorCentavos: money.Cents(l.ValorCentavos),
			Valor: money.Cents(l.ValorCentavos).BRL(), SaldoApos: money.Cents(l.SaldoApos).BRL(),
			OcorridoEm: l.OcorridoEm, ProjetadoEm: l.ProjetadoEm,
		})
	}
	return out, nil
}

func (m *Module) Defasagem(ctx context.Context) (int64, error) {
	return store.Defasagem(ctx, m.db.Pool)
}

// ---------------------------------------------------------------------------
// HTTP — o endpoint mais didático do projeto
// ---------------------------------------------------------------------------

// handleSaldo devolve os DOIS saldos lado a lado: o forte (soma sobre o log,
// dentro do núcleo) e o eventual (projeção, na borda). Logo depois de um Pix
// eles discordam por alguns milissegundos — e é exatamente esse o desenho.
func (m *Module) handleSaldo(w http.ResponseWriter, r *http.Request) {
	codigo := r.PathValue("codigo")

	forte, err := m.ledger.SaldoForte(r.Context(), codigo)
	if err != nil {
		httpx.Fail(w, http.StatusNotFound, "CONTA_DESCONHECIDA", err.Error(), nil)
		return
	}

	eventual, err := m.SaldoEventual(r.Context(), codigo)
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "ERRO_INTERNO", err.Error(), nil)
		return
	}

	defasagem, _ := m.Defasagem(r.Context())
	divergencia := forte - eventual.SaldoCentavos

	httpx.JSON(w, http.StatusOK, map[string]any{
		"conta": codigo,
		"forte": map[string]any{
			"saldo_centavos": forte,
			"saldo":          forte.BRL(),
			"fonte":          "SUM sobre o log de lancamentos (SERIALIZABLE)",
			"uso":            "e este saldo, e apenas este, que autoriza um pagamento",
		},
		"eventual": map[string]any{
			"saldo_centavos": eventual.SaldoCentavos,
			"saldo":          eventual.Saldo,
			"fonte":          "balances_projection (read model)",
			// Tempo desde a última vez que a projeção mexeu nesta conta.
			// Num sistema parado ele cresce sem que nada esteja atrasado —
			// quem mede atraso de verdade é `lancamentos_nao_projetados`.
			"ms_desde_ultima_projecao": eventual.AtrasoMs,
			"uso":                      "extrato, app, feed, notificacao",
		},
		"divergencia_centavos":    divergencia,
		"lancamentos_nao_projetados": defasagem,
		"nota": "divergencia temporaria aqui e ESCOLHA de arquitetura (§5.5): forte no nucleo, " +
			"eventual na borda. Ver o extrato 250ms atrasado nao machuca; debitar errado, sim.",
	})
}

func (m *Module) handleExtrato(w http.ResponseWriter, r *http.Request) {
	limite, _ := strconv.Atoi(r.URL.Query().Get("limite"))
	ls, err := m.Extrato(r.Context(), r.PathValue("codigo"), limite)
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "ERRO_INTERNO", err.Error(), nil)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"conta":   r.PathValue("codigo"),
		"linhas":  ls,
		"consistencia": "eventual",
	})
}

func (m *Module) handleProjecao(w http.ResponseWriter, r *http.Request) {
	defasagem, err := m.Defasagem(r.Context())
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "ERRO_INTERNO", err.Error(), nil)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"lancamentos_nao_projetados": defasagem,
		"atraso_proposital_ms":       m.atraso.Milliseconds(),
		"intervalo_ms":               m.intervalo.Milliseconds(),
		"nota": "se estas tabelas forem apagadas, o sistema se reconstroi do log. " +
			"Se `entries` for apagada, acabou. Essa assimetria e o ponto do CQRS aqui.",
	})
}
