// Package modular define a espinha do MONÓLITO MODULAR.
//
// Um processo, um deploy, um banco — e fronteiras internas REAIS.
// A fronteira aqui não é convenção nem lint: cada módulo guarda seu código
// interno em `.../<modulo>/internal/...`, e o compilador de Go PROÍBE que
// outro módulo importe de lá. Se um dia um módulo virar serviço, o corte já
// está desenhado (ADR-002).
package modular

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
)

// Module é o contrato de um bounded context dentro do monólito.
type Module interface {
	Nome() string
	Rotas(mux *http.ServeMux)
}

// Worker é o módulo que também roda processo de fundo (projeção, reconciliação).
type Worker interface {
	Iniciar(ctx context.Context)
}

// Checker permite ao módulo declarar se está apto a receber tráfego.
type Checker interface {
	Check(ctx context.Context) error
}

type Registry struct {
	mods []Module
}

func (r *Registry) Registrar(m Module) {
	r.mods = append(r.mods, m)
	slog.Info("modulo registrado", "modulo", m.Nome())
}

func (r *Registry) Montar(mux *http.ServeMux) {
	for _, m := range r.mods {
		m.Rotas(mux)
	}
}

// Iniciar sobe os workers de todos os módulos que os tenham.
func (r *Registry) Iniciar(ctx context.Context) {
	for _, m := range r.mods {
		if w, ok := m.(Worker); ok {
			slog.Info("worker do modulo iniciado", "modulo", m.Nome())
			w.Iniciar(ctx)
		}
	}
}

func (r *Registry) Check(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, m := range r.mods {
		if c, ok := m.(Checker); ok {
			if err := c.Check(ctx); err != nil {
				out[m.Nome()] = "erro: " + err.Error()
				continue
			}
		}
		out[m.Nome()] = "ok"
	}
	return out
}

func (r *Registry) Nomes() []string {
	out := make([]string, 0, len(r.mods))
	for _, m := range r.mods {
		out = append(out, m.Nome())
	}
	sort.Strings(out)
	return out
}
