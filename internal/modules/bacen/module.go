package bacen

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/wheslleyrimar/techpix/internal/modules/bacen/internal/breaker"
	"github.com/wheslleyrimar/techpix/internal/modules/bacen/internal/dict"
	"github.com/wheslleyrimar/techpix/internal/modules/bacen/internal/spi"
	"github.com/wheslleyrimar/techpix/internal/platform/httpx"
)

type Module struct {
	dict *dict.Client
	spi  *spi.Client
	ispb string
}

var (
	_ DICT = (*Module)(nil)
	_ SPI  = (*Module)(nil)
)

type Opcoes struct {
	BaseURL       string
	ISPB          string
	DictTimeout   time.Duration
	DictTTL       time.Duration
	DictTTLNeg    time.Duration
	BreakerFalhas int
	BreakerAberto time.Duration
	SPITimeout    time.Duration
}

func New(o Opcoes) *Module {
	return &Module{
		ispb: o.ISPB,
		dict: dict.New(dict.Opcoes{
			BaseURL:       o.BaseURL,
			ISPB:          o.ISPB,
			Timeout:       o.DictTimeout,
			TTL:           o.DictTTL,
			TTLNegativo:   o.DictTTLNeg,
			BreakerFalhas: o.BreakerFalhas,
			BreakerAberto: o.BreakerAberto,
		}),
		spi: spi.New(o.BaseURL, o.SPITimeout),
	}
}

func (m *Module) Nome() string { return "bacen" }

func (m *Module) Rotas(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/bacen/dict/{chave}", m.handleConsultarChave)
	mux.HandleFunc("GET /v1/bacen/estado", m.handleEstado)
	mux.HandleFunc("GET /v1/bacen/spi/{e2e}", m.handleConsultarSPI)
}

// Consultar traduz os erros do cliente para o vocabulário do domínio.
// Camada anticorrupção: o resto do sistema nunca vê "HTTP 429".
func (m *Module) Consultar(chave string) (*ChavePix, error) {
	e, doCache, err := m.dict.Consultar(chave)
	switch {
	case errors.Is(err, dict.ErrInvalida):
		return nil, fmt.Errorf("%w: %v", ErrChaveInvalida, err)
	case errors.Is(err, dict.ErrNaoEncontrada):
		return nil, ErrChaveNaoEncontrada
	case errors.Is(err, dict.ErrRateLimited):
		return nil, ErrDictRateLimited
	case errors.Is(err, dict.ErrTimeout):
		return nil, ErrDictTimeout
	case errors.Is(err, breaker.ErrCircuitoAberto):
		return nil, fmt.Errorf("%w: circuito aberto", ErrDictIndisponivel)
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrDictIndisponivel, err)
	}

	return &ChavePix{
		Chave: e.Chave, TipoChave: e.TipoChave, ISPB: e.ISPB, Instituicao: e.Instituicao,
		Agencia: e.Agencia, Conta: e.Conta, TipoConta: e.TipoConta,
		Titular: e.Titular, CPFCNPJ: e.CPFCNPJ,
		ConsultadoEm: time.Now(), DoCache: doCache,
	}, nil
}

func (m *Module) Estado() map[string]any {
	return map[string]any{
		"ispb_techpix": m.ispb,
		"dict":         m.dict.Estado(),
	}
}

func (m *Module) Enviar(req Pacs008) (*Pacs002, error) {
	r, err := m.spi.Enviar(spi.Pacs008{
		E2EID: req.E2EID, ISPBPagador: req.ISPBPagador, ISPBRecebedor: req.ISPBRecebedor,
		ChaveRecebedor: req.ChaveRecebedor, ValorCentavos: req.ValorCentavos, Descricao: req.Descricao,
	})
	return traduzirSPI(r, err)
}

// ConsultarStatus tem nome diferente de Consultar (DICT) porque as duas
// interfaces convivem no mesmo módulo — DICT e SPI são trilhos distintos.
func (m *Module) ConsultarStatus(e2eID string) (*Pacs002, error) {
	r, err := m.spi.Consultar(e2eID)
	return traduzirSPI(r, err)
}

func traduzirSPI(r *spi.Pacs002, err error) (*Pacs002, error) {
	switch {
	case errors.Is(err, spi.ErrTimeout):
		return nil, ErrSPITimeout
	case errors.Is(err, spi.ErrNaoEncontrado):
		return nil, ErrSPINaoEncontrado
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrSPIIndisponivel, err)
	}
	return &Pacs002{E2EID: r.E2EID, Status: r.Status, Motivo: r.Motivo, LiquidadoEm: r.LiquidadoEm}, nil
}

// ---------------------------------------------------------------------------
// HTTP — endpoints de inspeção, úteis ao vivo em aula
// ---------------------------------------------------------------------------

func (m *Module) handleConsultarChave(w http.ResponseWriter, r *http.Request) {
	inicio := time.Now()
	e, err := m.Consultar(r.PathValue("chave"))
	ms := float64(time.Since(inicio).Microseconds()) / 1000

	switch {
	case errors.Is(err, ErrChaveInvalida):
		httpx.Fail(w, http.StatusBadRequest, "CHAVE_INVALIDA",
			err.Error()+" (bloqueada localmente: nenhum token do DICT foi gasto)", nil)
	case errors.Is(err, ErrChaveNaoEncontrada):
		httpx.Fail(w, http.StatusNotFound, "CHAVE_NAO_ENCONTRADA",
			"chave inexistente no DICT (custou 20 tokens do balde)", nil)
	case errors.Is(err, ErrDictRateLimited):
		httpx.Fail(w, http.StatusTooManyRequests, "DICT_RATE_LIMITED", err.Error(), nil)
	case err != nil:
		httpx.Fail(w, http.StatusServiceUnavailable, "DICT_INDISPONIVEL", err.Error(), nil)
	default:
		httpx.JSON(w, http.StatusOK, map[string]any{"chave": e, "latencia_ms": ms})
	}
}

func (m *Module) handleEstado(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, m.Estado())
}

func (m *Module) handleConsultarSPI(w http.ResponseWriter, r *http.Request) {
	res, err := m.ConsultarStatus(r.PathValue("e2e"))
	if errors.Is(err, ErrSPINaoEncontrado) {
		httpx.Fail(w, http.StatusNotFound, "E2E_DESCONHECIDO", "o SPI nao conhece este EndToEndId", nil)
		return
	}
	if err != nil {
		httpx.Fail(w, http.StatusServiceUnavailable, "SPI_INDISPONIVEL", err.Error(), nil)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
