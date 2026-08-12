// Package breaker é o circuit breaker do caminho crítico.
//
// Aula 1 · §6.4: o DICT está no caminho crítico e é síncrono. Ele acopla a
// nossa disponibilidade à de um sistema externo. "Timeout agressivo, circuit
// breaker e fallback decidem se um soluço no DICT derruba pagamentos inteiros."
//
// Sem breaker, um DICT lento vira fila local: cada requisição segura uma
// conexão por 1s, o pool esgota (Lei de Little), e a fintech inteira para —
// por causa de um sistema que não é nosso.
package breaker

import (
	"errors"
	"sync"
	"time"
)

type Estado string

const (
	Fechado    Estado = "fechado"     // tudo passa
	Aberto     Estado = "aberto"      // tudo falha rápido, sem tocar no externo
	MeioAberto Estado = "meio_aberto" // deixa UMA passar para testar a água
)

var ErrCircuitoAberto = errors.New("circuito aberto: dependencia externa considerada indisponivel")

type Breaker struct {
	mu             sync.Mutex
	estado         Estado
	falhas         int
	limiteFalhas   int
	tempoAberto    time.Duration
	abertoDesde    time.Time
	trocasDeEstado int
}

func New(limiteFalhas int, tempoAberto time.Duration) *Breaker {
	if limiteFalhas < 1 {
		limiteFalhas = 5
	}
	return &Breaker{estado: Fechado, limiteFalhas: limiteFalhas, tempoAberto: tempoAberto}
}

// Executar roda fn se o circuito permitir. Falhar rápido é melhor que
// esperar por quem já demonstrou estar fora do ar.
func (b *Breaker) Executar(fn func() error) error {
	if err := b.permitir(); err != nil {
		return err
	}
	err := fn()
	b.registrar(err)
	return err
}

func (b *Breaker) permitir() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.estado == Aberto {
		if time.Since(b.abertoDesde) < b.tempoAberto {
			return ErrCircuitoAberto
		}
		b.estado = MeioAberto
		b.trocasDeEstado++
	}
	return nil
}

func (b *Breaker) registrar(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err == nil {
		b.falhas = 0
		if b.estado != Fechado {
			b.estado = Fechado
			b.trocasDeEstado++
		}
		return
	}

	b.falhas++
	if b.estado == MeioAberto || b.falhas >= b.limiteFalhas {
		b.estado = Aberto
		b.abertoDesde = time.Now()
		b.trocasDeEstado++
	}
}

func (b *Breaker) Estado() Estado {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.estado
}

func (b *Breaker) Snapshot() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]any{
		"estado":            string(b.estado),
		"falhas_seguidas":   b.falhas,
		"limite_falhas":     b.limiteFalhas,
		"tempo_aberto_ms":   b.tempoAberto.Milliseconds(),
		"trocas_de_estado":  b.trocasDeEstado,
	}
}
