// Package httpx tem o mínimo de encanamento HTTP compartilhado entre os módulos.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/wheslleyrimar/techpix/internal/platform/ids"
	"github.com/wheslleyrimar/techpix/internal/platform/obs"
)

// Erro é a forma única de erro da API. Código estável em MAIÚSCULA para o
// cliente decidir programaticamente se vale a pena reenviar.
type Erro struct {
	Codigo    string         `json:"codigo"`
	Mensagem  string         `json:"mensagem"`
	Detalhes  map[string]any `json:"detalhes,omitempty"`
	Retentavel bool          `json:"retentavel"`
}

type respostaErro struct {
	Erro Erro `json:"erro"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		slog.Error("falha ao serializar resposta", "erro", err)
	}
}

func Fail(w http.ResponseWriter, status int, codigo, mensagem string, detalhes map[string]any) {
	obs.Inc("http.erro." + codigo)
	JSON(w, status, respostaErro{Erro{
		Codigo:     codigo,
		Mensagem:   mensagem,
		Detalhes:   detalhes,
		Retentavel: status >= 500 || status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable,
	}})
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		Fail(w, http.StatusBadRequest, "PAYLOAD_INVALIDO", "corpo da requisicao invalido: "+err.Error(), nil)
		return false
	}
	return true
}

// Middleware padrão: request id, log estruturado e recover.
// Falhar fechado também vale para panic: 500 e nada de dinheiro se movendo.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-Id")
		if rid == "" {
			rid = ids.NewUUID()
		}
		w.Header().Set("X-Request-Id", rid)

		sw := &statusWriter{ResponseWriter: w, status: 200}
		inicio := time.Now()

		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic no handler", "erro", rec, "rota", r.URL.Path, "request_id", rid)
				obs.Inc("http.panic")
				Fail(sw, http.StatusInternalServerError, "ERRO_INTERNO",
					"falha interna; nenhuma operacao financeira foi confirmada", nil)
			}
			d := time.Since(inicio)
			obs.Observe("http "+r.Method+" "+rotaLimpa(r), d)
			slog.Info("http",
				"metodo", r.Method, "rota", r.URL.Path, "status", sw.status,
				"ms", float64(d.Microseconds())/1000, "request_id", rid)
		}()

		next.ServeHTTP(sw, r)
	})
}

func rotaLimpa(r *http.Request) string {
	if p := r.Pattern; p != "" {
		return p
	}
	return r.URL.Path
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
