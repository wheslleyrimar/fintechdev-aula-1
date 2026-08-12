// Package ui serve o painel da aula.
//
// É um módulo como qualquer outro do monólito: entra no registry, declara suas
// rotas, e não sabe nada sobre ledger, Pix ou BACEN — fala só HTTP com a
// própria API pública do sistema, igual a qualquer cliente externo faria.
//
// Sem npm, sem bundler, sem CDN: HTML, CSS e JS embutidos no binário. Uma
// stack de aula que exige `npm install` na frente de 40 pessoas é uma stack
// que vai falhar na frente de 40 pessoas.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/wheslleyrimar/techpix/internal/platform/httpx"
)

//go:embed assets
var assets embed.FS

type Module struct {
	arquivos http.Handler
}

func New() *Module {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("assets do painel ausentes: " + err.Error())
	}
	return &Module{arquivos: http.FileServer(http.FS(sub))}
}

func (m *Module) Nome() string { return "ui" }

// Rotas registra o catch-all "/". Como o ServeMux do Go escolhe sempre o
// padrão MAIS específico, todas as rotas /v1/... continuam ganhando desta.
func (m *Module) Rotas(mux *http.ServeMux) {
	mux.HandleFunc("GET /", m.servir)
}

func (m *Module) servir(w http.ResponseWriter, r *http.Request) {
	caminho := strings.TrimPrefix(r.URL.Path, "/")

	if caminho == "" || caminho == "index.html" {
		// Servido na mão: o http.FileServer redirecionaria /index.html para "./"
		// e o navegador ficaria pingando 301 antes de ver a página.
		pagina, err := assets.ReadFile("assets/index.html")
		if err != nil {
			httpx.Fail(w, http.StatusInternalServerError, "PAINEL_INDISPONIVEL", err.Error(), nil)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(pagina)
		return
	}

	if _, err := fs.Stat(assets, "assets/"+caminho); err != nil {
		// Quem errou uma rota de API merece resposta de API, não uma página.
		httpx.Fail(w, http.StatusNotFound, "ROTA_DESCONHECIDA", "rota inexistente: "+r.URL.Path, nil)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	m.arquivos.ServeHTTP(w, r)
}
