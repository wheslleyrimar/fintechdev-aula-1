// Package obs implementa duas coisas que a Aula 1 trata como decisão de
// arquitetura, não como "monitoramento":
//
//  1. Orçamento de latência (§6.6): o teto do Pix é 40s. Cada passo consome
//     uma fatia. O trabalho do arquiteto é DISTRIBUIR e DEFENDER esse orçamento
//     — e para defender, primeiro é preciso medir passo a passo.
//
//  2. Latência é distribuição, não número (§5.6): guardamos amostras e
//     reportamos p50/p99/p99.9. Média engana; em fintech a CAUDA manda, porque
//     é nela que o cliente desiste, o timeout dispara e o retry nasce.
package obs

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// TetoNormativoPix é o teto de ponta a ponta do canal primário do SPI
// (t0' -> t4), Resolução BCB nº 195/2022. Passou disso, é rejeitado.
const TetoNormativoPix = 40 * time.Second

// ---------------------------------------------------------------------------
// Orçamento de latência de UMA requisição
// ---------------------------------------------------------------------------

type Passo struct {
	Nome string  `json:"nome"`
	Ms   float64 `json:"ms"`
}

type Budget struct {
	inicio time.Time
	passos []Passo
}

func NewBudget() *Budget { return &Budget{inicio: time.Now()} }

// Track cronometra um passo, registra na métrica global e devolve o erro do passo.
func (b *Budget) Track(nome string, fn func() error) error {
	t0 := time.Now()
	err := fn()
	d := time.Since(t0)
	b.passos = append(b.passos, Passo{Nome: nome, Ms: msOf(d)})
	Observe(nome, d)
	return err
}

func (b *Budget) Marcar(nome string, d time.Duration) {
	b.passos = append(b.passos, Passo{Nome: nome, Ms: msOf(d)})
	Observe(nome, d)
}

func (b *Budget) Total() time.Duration { return time.Since(b.inicio) }

// JSON é devolvido em toda resposta de pagamento. O aluno vê, na prática,
// para onde foi cada milissegundo do orçamento.
func (b *Budget) JSON() map[string]any {
	total := b.Total()
	return map[string]any{
		"passos":            b.passos,
		"total_ms":          msOf(total),
		"teto_normativo_ms": msOf(TetoNormativoPix),
		"consumo_do_teto":   fmt.Sprintf("%.2f%%", float64(total)/float64(TetoNormativoPix)*100),
	}
}

func msOf(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

// ---------------------------------------------------------------------------
// Métricas globais (histogramas por passo + contadores)
// ---------------------------------------------------------------------------

const maxAmostras = 4096

type hist struct {
	mu       sync.Mutex
	amostras []float64
	total    uint64
	idx      int
}

var (
	histsMu   sync.RWMutex
	hists     = map[string]*hist{}
	countsMu  sync.RWMutex
	counts    = map[string]*atomic.Int64{}
)

func Observe(nome string, d time.Duration) {
	histsMu.RLock()
	h := hists[nome]
	histsMu.RUnlock()
	if h == nil {
		histsMu.Lock()
		if h = hists[nome]; h == nil {
			h = &hist{amostras: make([]float64, 0, 256)}
			hists[nome] = h
		}
		histsMu.Unlock()
	}

	v := msOf(d)
	h.mu.Lock()
	h.total++
	if len(h.amostras) < maxAmostras {
		h.amostras = append(h.amostras, v)
	} else { // reservatório circular: mantém memória constante
		h.amostras[h.idx] = v
		h.idx = (h.idx + 1) % maxAmostras
	}
	h.mu.Unlock()
}

func Inc(nome string) { Add(nome, 1) }

func Add(nome string, n int64) {
	countsMu.RLock()
	c := counts[nome]
	countsMu.RUnlock()
	if c == nil {
		countsMu.Lock()
		if c = counts[nome]; c == nil {
			c = &atomic.Int64{}
			counts[nome] = c
		}
		countsMu.Unlock()
	}
	c.Add(n)
}

type Stat struct {
	Amostras uint64  `json:"amostras"`
	P50      float64 `json:"p50_ms"`
	P99      float64 `json:"p99_ms"`
	P999     float64 `json:"p999_ms"`
	Max      float64 `json:"max_ms"`
}

func Snapshot() (map[string]Stat, map[string]int64) {
	histsMu.RLock()
	nomes := make([]string, 0, len(hists))
	for n := range hists {
		nomes = append(nomes, n)
	}
	histsMu.RUnlock()

	out := make(map[string]Stat, len(nomes))
	for _, n := range nomes {
		histsMu.RLock()
		h := hists[n]
		histsMu.RUnlock()

		h.mu.Lock()
		cp := append([]float64(nil), h.amostras...)
		total := h.total
		h.mu.Unlock()

		if len(cp) == 0 {
			continue
		}
		sort.Float64s(cp)
		out[n] = Stat{
			Amostras: total,
			P50:      quantil(cp, 0.50),
			P99:      quantil(cp, 0.99),
			P999:     quantil(cp, 0.999),
			Max:      cp[len(cp)-1],
		}
	}

	countsMu.RLock()
	cs := make(map[string]int64, len(counts))
	for k, v := range counts {
		cs[k] = v.Load()
	}
	countsMu.RUnlock()

	return out, cs
}

func quantil(ordenado []float64, q float64) float64 {
	if len(ordenado) == 0 {
		return 0
	}
	i := int(q * float64(len(ordenado)-1))
	if i < 0 {
		i = 0
	}
	if i >= len(ordenado) {
		i = len(ordenado) - 1
	}
	return ordenado[i]
}

// WriteProm cospe formato Prometheus text — o suficiente para a Aula 2 plugar
// um scraper e começar a "decidir na evidência".
func WriteProm(w io.Writer) {
	stats, cs := Snapshot()
	fmt.Fprintln(w, "# HELP techpix_step_latency_ms Latencia por passo do orcamento (quantis)")
	fmt.Fprintln(w, "# TYPE techpix_step_latency_ms gauge")
	nomes := make([]string, 0, len(stats))
	for n := range stats {
		nomes = append(nomes, n)
	}
	sort.Strings(nomes)
	for _, n := range nomes {
		s := stats[n]
		fmt.Fprintf(w, "techpix_step_latency_ms{passo=%q,quantil=\"0.5\"} %.3f\n", n, s.P50)
		fmt.Fprintf(w, "techpix_step_latency_ms{passo=%q,quantil=\"0.99\"} %.3f\n", n, s.P99)
		fmt.Fprintf(w, "techpix_step_latency_ms{passo=%q,quantil=\"0.999\"} %.3f\n", n, s.P999)
		fmt.Fprintf(w, "techpix_step_total{passo=%q} %d\n", n, s.Amostras)
	}
	fmt.Fprintln(w, "# TYPE techpix_eventos_total counter")
	ks := make([]string, 0, len(cs))
	for k := range cs {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		fmt.Fprintf(w, "techpix_eventos_total{evento=%q} %d\n", k, cs[k])
	}
}
