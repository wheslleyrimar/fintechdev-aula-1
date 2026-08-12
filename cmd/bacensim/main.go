// Comando bacensim: um BACEN de mentira, com as regras de verdade.
//
// Existe para que a aula sinta na pele o que a §6 descreve:
//
//   - DICT protegido por TOKEN BUCKET, onde um 404 custa 20 tokens e um 200
//     custa 1 — design de segurança por incentivo: varrer o diretório fica
//     caro, usar normalmente não.
//   - SLA de latência: o DICT responde dentro de ~p99 1s; o SPI leva segundos.
//   - SPI que liquida em moeda de banco central, de forma FINAL e irrevogável,
//     e que exige E2E ID único.
//   - E o modo mais importante da aula: BURACO NEGRO. O SPI liquida e a
//     resposta nunca chega. É o timeout ambíguo do §4.1, ao vivo.
//
// Nada aqui é persistente: é um simulador de aula, não um participante do SPB.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// DICT
// ---------------------------------------------------------------------------

type entradaDICT struct {
	Chave       string `json:"chave"`
	TipoChave   string `json:"tipo_chave"`
	ISPB        string `json:"ispb"`
	Instituicao string `json:"instituicao"`
	Agencia     string `json:"agencia"`
	Conta       string `json:"conta"`
	TipoConta   string `json:"tipo_conta"`
	Titular     string `json:"titular"`
	CPFCNPJ     string `json:"cpf_cnpj"`
}

// Diretório semente. CPFs são válidos no dígito verificador de propósito:
// o cliente do TechPix valida localmente antes de gastar token.
var diretorio = map[string]entradaDICT{
	"bruno@bancobeta.com.br": {TipoChave: "EMAIL", ISPB: "00000002", Instituicao: "Banco Beta",
		Agencia: "0001", Conta: "334455-6", TipoConta: "CACC", Titular: "Bruno Alves", CPFCNPJ: "12345678909"},
	"+5521988887777": {TipoChave: "TELEFONE", ISPB: "00000002", Instituicao: "Banco Beta",
		Agencia: "0001", Conta: "334455-6", TipoConta: "CACC", Titular: "Bruno Alves", CPFCNPJ: "12345678909"},
	"12345678909": {TipoChave: "CPF", ISPB: "00000002", Instituicao: "Banco Beta",
		Agencia: "0001", Conta: "334455-6", TipoConta: "CACC", Titular: "Bruno Alves", CPFCNPJ: "12345678909"},
	"3f1b9d5e-7c42-4a1b-9f2d-6e5a4c3b2a10": {TipoChave: "EVP", ISPB: "00000003", Instituicao: "Banco Gama",
		Agencia: "0007", Conta: "778899-0", TipoConta: "SVGS", Titular: "Marina Costa", CPFCNPJ: "52998224725"},

	// Chaves do próprio TechPix (Pix entre clientes da casa também passa pelo SPI).
	"+5511999990001": {TipoChave: "TELEFONE", ISPB: "00000001", Instituicao: "TechPix",
		Agencia: "0001", Conta: "000010-1", TipoConta: "CACC", Titular: "Ana Souza", CPFCNPJ: "11144477735"},
	"joao@techpix.com.br": {TipoChave: "EMAIL", ISPB: "00000001", Instituicao: "TechPix",
		Agencia: "0001", Conta: "000011-2", TipoConta: "CACC", Titular: "Joao Lima", CPFCNPJ: "52998224725"},
	"39053344705": {TipoChave: "CPF", ISPB: "00000001", Instituicao: "TechPix",
		Agencia: "0001", Conta: "000012-3", TipoConta: "CACC", Titular: "Carla Dias", CPFCNPJ: "39053344705"},

	// Conta marcada em lista restritiva — para a demo de PLD-FT (falhar fechado).
	"golpista@fraude.com": {TipoChave: "EMAIL", ISPB: "00000009", Instituicao: "Banco Omega",
		Agencia: "0001", Conta: "999999-9", TipoConta: "CACC", Titular: "Conta Suspeita", CPFCNPJ: "99999999999"},
}

// balde é o token bucket do §6.4. Um por participante (ISPB).
type balde struct {
	tokens     float64
	capacidade float64
	porMinuto  float64
	ultimo     time.Time
	consumidos float64
	bloqueios  int
}

func (b *balde) repor(agora time.Time) {
	dt := agora.Sub(b.ultimo).Seconds()
	b.ultimo = agora
	b.tokens = math.Min(b.capacidade, b.tokens+dt*(b.porMinuto/60.0))
}

// tomar cobra `custo` tokens. Se não houver, é HTTP 429.
func (b *balde) tomar(custo float64) bool {
	b.repor(time.Now())
	if b.tokens < custo {
		b.bloqueios++
		return false
	}
	b.tokens -= custo
	b.consumidos += custo
	return true
}

type simulador struct {
	mu     sync.Mutex
	baldes map[string]*balde

	// Configuração do DICT
	tokensPorMinuto float64
	capacidade      float64
	custoOK         float64
	custo404        float64
	dictP99         time.Duration

	// Configuração do SPI
	spiP50        time.Duration
	spiP99        time.Duration
	taxaRejeicao  float64
	taxaBuracoNegro float64

	// "Ledger" do SPI: E2E ID -> desfecho. O SPI exige unicidade do E2E ID,
	// e é por isso que reenviar a mesma instrução não paga duas vezes.
	pagamentos map[string]pacs002
}

type pacs008 struct {
	E2EID          string `json:"e2e_id"`
	ISPBPagador    string `json:"ispb_pagador"`
	ISPBRecebedor  string `json:"ispb_recebedor"`
	ChaveRecebedor string `json:"chave_recebedor"`
	ValorCentavos  int64  `json:"valor_centavos"`
	Descricao      string `json:"descricao"`
}

type pacs002 struct {
	E2EID       string    `json:"e2e_id"`
	Status      string    `json:"status"`
	Motivo      string    `json:"motivo,omitempty"`
	LiquidadoEm time.Time `json:"liquidado_em"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	s := &simulador{
		baldes:          map[string]*balde{},
		pagamentos:      map[string]pacs002{},
		tokensPorMinuto: envFloat("DICT_TOKENS_PER_MIN", 60),
		capacidade:      envFloat("DICT_BUCKET_SIZE", 100),
		custoOK:         envFloat("DICT_COST_OK", 1),
		custo404:        envFloat("DICT_COST_NOT_FOUND", 20),
		dictP99:         time.Duration(envFloat("DICT_P99_MS", 300)) * time.Millisecond,
		taxaRejeicao:    envFloat("SPI_REJECT_RATE", 0),
		taxaBuracoNegro: envFloat("SPI_BLACKHOLE_RATE", 0),
	}

	if strings.EqualFold(os.Getenv("SPI_REALISTIC"), "true") {
		// Números reais do SPI: p50 2,8s e p99 4,6s (Manual de Tempos do Pix).
		s.spiP50, s.spiP99 = 2800*time.Millisecond, 4600*time.Millisecond
	} else {
		s.spiP50 = time.Duration(envFloat("SPI_P50_MS", 280)) * time.Millisecond
		s.spiP99 = time.Duration(envFloat("SPI_P99_MS", 460)) * time.Millisecond
	}

	// Normaliza as chaves do diretório (a chave do mapa é a própria chave Pix).
	for k, v := range diretorio {
		v.Chave = k
		diretorio[k] = v
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		escreverJSON(w, 200, map[string]string{"status": "ok", "papel": "simulador BACEN (DICT + SPI)"})
	})
	mux.HandleFunc("GET /dict/v1/keys/{chave}", s.consultarChave)
	mux.HandleFunc("GET /dict/v1/baldes", s.verBaldes)
	mux.HandleFunc("POST /spi/v1/pacs008", s.liquidar)
	mux.HandleFunc("GET /spi/v1/payments/{e2e}", s.consultarPagamento)
	mux.HandleFunc("GET /admin/config", s.verConfig)
	mux.HandleFunc("POST /admin/config", s.mudarConfig)

	porta := os.Getenv("PORT")
	if porta == "" {
		porta = "9090"
	}

	slog.Info("simulador do BACEN no ar",
		"porta", porta,
		"dict_tokens_por_minuto", s.tokensPorMinuto,
		"dict_balde", s.capacidade,
		"custo_404", s.custo404,
		"spi_p50_ms", s.spiP50.Milliseconds(),
		"spi_p99_ms", s.spiP99.Milliseconds())

	srv := &http.Server{Addr: ":" + porta, Handler: cors(mux), ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("simulador caiu", "erro", err)
		os.Exit(1)
	}
}

// cors libera o painel da aula (servido em outra porta) a falar com o
// simulador direto do navegador.
//
// Isto é uma concessão do SIMULADOR, não do TechPix: o painel precisa mexer nos
// parâmetros do "BACEN" ao vivo. Um participante real nunca teria essa porta —
// e é justamente por não ter que a reconciliação existe.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-ISPB")
		w.Header().Set("Access-Control-Expose-Headers", "X-Tokens-Restantes, X-Custo-Consulta, X-Duplicado")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// DICT: consulta com token bucket
// ---------------------------------------------------------------------------

func (s *simulador) consultarChave(w http.ResponseWriter, r *http.Request) {
	chave := r.PathValue("chave")
	ispb := r.Header.Get("X-ISPB")
	if ispb == "" {
		ispb = "desconhecido"
	}

	entrada, existe := diretorio[chave]
	custo := s.custoOK
	if !existe {
		// A assimetria que desestimula varredura: um 404 sai 20x mais caro.
		custo = s.custo404
	}

	s.mu.Lock()
	b, ok := s.baldes[ispb]
	if !ok {
		b = &balde{tokens: s.capacidade, capacidade: s.capacidade, porMinuto: s.tokensPorMinuto, ultimo: time.Now()}
		s.baldes[ispb] = b
	}
	permitido := b.tomar(custo)
	restantes := b.tokens
	s.mu.Unlock()

	w.Header().Set("X-Tokens-Restantes", fmt.Sprintf("%.1f", restantes))
	w.Header().Set("X-Custo-Consulta", fmt.Sprintf("%.0f", custo))

	if !permitido {
		slog.Warn("DICT: balde esgotado", "ispb", ispb, "chave", chave, "custo", custo)
		escreverJSON(w, http.StatusTooManyRequests, map[string]any{
			"erro":    "RATE_LIMITED",
			"detalhe": "balde de tokens esgotado para este participante",
			"dica":    "consultas 404 custam 20 tokens: valide a chave localmente e use cache",
		})
		return
	}

	dormir(s.dictP99 / 3) // dentro do SLA: p99 <= 1s

	if !existe {
		escreverJSON(w, http.StatusNotFound, map[string]any{
			"erro":         "CHAVE_NAO_ENCONTRADA",
			"custo_tokens": custo,
		})
		return
	}
	escreverJSON(w, http.StatusOK, entrada)
}

func (s *simulador) verBaldes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := map[string]any{}
	for ispb, b := range s.baldes {
		b.repor(time.Now())
		out[ispb] = map[string]any{
			"tokens_disponiveis": math.Round(b.tokens*10) / 10,
			"capacidade":         b.capacidade,
			"reposicao_por_min":  b.porMinuto,
			"tokens_consumidos":  b.consumidos,
			"bloqueios_429":      b.bloqueios,
		}
	}
	escreverJSON(w, 200, map[string]any{
		"baldes": out,
		"politica": map[string]any{
			"custo_http_200": s.custoOK,
			"custo_http_404": s.custo404,
			"nota":           "custo assimetrico pune varredura sem punir uso normal",
		},
	})
}

// ---------------------------------------------------------------------------
// SPI: liquidação em moeda de banco central
// ---------------------------------------------------------------------------

func (s *simulador) liquidar(w http.ResponseWriter, r *http.Request) {
	var req pacs008
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		escreverJSON(w, http.StatusBadRequest, map[string]string{"erro": "pacs.008 invalido"})
		return
	}
	if len(req.E2EID) != 32 {
		escreverJSON(w, http.StatusBadRequest, map[string]string{"erro": "EndToEndId deve ter 32 caracteres"})
		return
	}

	// Regra do BACEN: E2E ID é único. Reenviar a mesma instrução devolve o
	// MESMO desfecho — o SPI não paga duas vezes. É idempotência imposta pelo
	// regulador, não gentileza do participante.
	s.mu.Lock()
	if anterior, existe := s.pagamentos[req.E2EID]; existe {
		s.mu.Unlock()
		slog.Info("SPI: pacs.008 repetido; devolvendo o desfecho original", "e2e_id", req.E2EID)
		w.Header().Set("X-Duplicado", "true")
		escreverJSON(w, http.StatusOK, anterior)
		return
	}
	rejeitar := rand.Float64() < s.taxaRejeicao
	buracoNegro := rand.Float64() < s.taxaBuracoNegro
	s.mu.Unlock()

	espera := amostraLognormal(s.spiP50, s.spiP99)

	resp := pacs002{E2EID: req.E2EID, Status: "ACSC", LiquidadoEm: time.Now()}
	if rejeitar {
		resp.Status = "RJCT"
		resp.Motivo = "AB09: falha no participante recebedor"
	}

	if buracoNegro {
		// O caso mais importante da aula: a liquidação ACONTECEU (final e
		// irrevogável), mas a resposta nunca chega. Do lado do participante,
		// isso é apenas um timeout — indistinguível de "não aconteceu nada".
		// Só a reconciliação por E2E ID desfaz essa ambiguidade.
		s.mu.Lock()
		s.pagamentos[req.E2EID] = resp
		s.mu.Unlock()
		slog.Warn("SPI: BURACO NEGRO — liquidado, resposta retida de proposito", "e2e_id", req.E2EID)
		dormir(90 * time.Second)
		return
	}

	dormir(espera)

	s.mu.Lock()
	s.pagamentos[req.E2EID] = resp
	s.mu.Unlock()

	slog.Info("SPI: liquidado em moeda de banco central",
		"e2e_id", req.E2EID, "status", resp.Status, "centavos", req.ValorCentavos,
		"ispb_pagador", req.ISPBPagador, "ispb_recebedor", req.ISPBRecebedor)

	escreverJSON(w, http.StatusOK, resp)
}

// consultarPagamento é o que salva o participante quando a resposta se perde:
// o desfecho sempre existiu, só não tinha chegado.
func (s *simulador) consultarPagamento(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	p, ok := s.pagamentos[r.PathValue("e2e")]
	s.mu.Unlock()

	if !ok {
		escreverJSON(w, http.StatusNotFound, map[string]string{
			"erro":    "E2E_DESCONHECIDO",
			"detalhe": "o SPI nunca registrou esta instrucao: nada foi liquidado",
		})
		return
	}
	escreverJSON(w, http.StatusOK, p)
}

// ---------------------------------------------------------------------------
// Admin: mexer nos parâmetros AO VIVO durante a aula
// ---------------------------------------------------------------------------

func (s *simulador) verConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	escreverJSON(w, 200, map[string]any{
		"dict_tokens_por_minuto": s.tokensPorMinuto,
		"dict_capacidade":        s.capacidade,
		"dict_custo_ok":          s.custoOK,
		"dict_custo_404":         s.custo404,
		"spi_p50_ms":             s.spiP50.Milliseconds(),
		"spi_p99_ms":             s.spiP99.Milliseconds(),
		"spi_taxa_rejeicao":      s.taxaRejeicao,
		"spi_taxa_buraco_negro":  s.taxaBuracoNegro,
		"pagamentos_liquidados":  len(s.pagamentos),
	})
}

type mudanca struct {
	SPIP50Ms        *int64   `json:"spi_p50_ms"`
	SPIP99Ms        *int64   `json:"spi_p99_ms"`
	TaxaRejeicao    *float64 `json:"spi_taxa_rejeicao"`
	TaxaBuracoNegro *float64 `json:"spi_taxa_buraco_negro"`
	TokensPorMinuto *float64 `json:"dict_tokens_por_minuto"`
	Capacidade      *float64 `json:"dict_capacidade"`
	ZerarBaldes     bool     `json:"zerar_baldes"`
}

func (s *simulador) mudarConfig(w http.ResponseWriter, r *http.Request) {
	var m mudanca
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		escreverJSON(w, http.StatusBadRequest, map[string]string{"erro": "json invalido"})
		return
	}

	s.mu.Lock()
	if m.SPIP50Ms != nil {
		s.spiP50 = time.Duration(*m.SPIP50Ms) * time.Millisecond
	}
	if m.SPIP99Ms != nil {
		s.spiP99 = time.Duration(*m.SPIP99Ms) * time.Millisecond
	}
	if m.TaxaRejeicao != nil {
		s.taxaRejeicao = *m.TaxaRejeicao
	}
	if m.TaxaBuracoNegro != nil {
		s.taxaBuracoNegro = *m.TaxaBuracoNegro
	}
	if m.TokensPorMinuto != nil {
		s.tokensPorMinuto = *m.TokensPorMinuto
		for _, b := range s.baldes {
			b.porMinuto = s.tokensPorMinuto
		}
	}
	if m.Capacidade != nil {
		s.capacidade = *m.Capacidade
		for _, b := range s.baldes {
			b.capacidade = s.capacidade
		}
	}
	if m.ZerarBaldes {
		s.baldes = map[string]*balde{}
	}
	s.mu.Unlock()

	slog.Info("configuracao do simulador alterada ao vivo")
	s.verConfig(w, r)
}

// ---------------------------------------------------------------------------
// Utilidades
// ---------------------------------------------------------------------------

// amostraLognormal sorteia latência a partir de p50 e p99.
// Latência de sistema real não é constante nem uniforme: tem cauda longa.
// Simular com cauda é o que faz p99 e p50 divergirem, como na vida.
func amostraLognormal(p50, p99 time.Duration) time.Duration {
	if p50 <= 0 {
		return 0
	}
	mu := math.Log(float64(p50))
	sigma := (math.Log(float64(p99)) - mu) / 2.326 // z(0.99) ≈ 2.326
	if sigma < 0 {
		sigma = 0
	}
	v := math.Exp(mu + sigma*rand.NormFloat64())
	if v > float64(30*time.Second) {
		v = float64(30 * time.Second)
	}
	return time.Duration(v)
}

func dormir(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

func escreverJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func envFloat(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
