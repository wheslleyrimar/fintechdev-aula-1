// Package dict é o cliente do diretório de chaves do BACEN.
//
// Três decisões de arquitetura moram neste arquivo, todas da §6.4:
//
//  1. VALIDAÇÃO LOCAL antes de consultar. Um 404 custa 20 tokens; um 200 custa 1.
//     Chave malformada nunca deve virar requisição — é jogar 20 tokens fora.
//  2. CACHE disciplinado (positivo e negativo). Chave é recurso caro.
//  3. TIMEOUT curto + CIRCUIT BREAKER. O DICT está no caminho crítico e é
//     síncrono; o SLA dele é p99 ≤ 1s. Esperar mais que isso é importar a
//     indisponibilidade dos outros para dentro de casa.
package dict

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wheslleyrimar/techpix/internal/modules/bacen/internal/breaker"
	"github.com/wheslleyrimar/techpix/internal/platform/obs"
)

type Entrada struct {
	Chave       string    `json:"chave"`
	TipoChave   string    `json:"tipo_chave"`
	ISPB        string    `json:"ispb"`
	Instituicao string    `json:"instituicao"`
	Agencia     string    `json:"agencia"`
	Conta       string    `json:"conta"`
	TipoConta   string    `json:"tipo_conta"`
	Titular     string    `json:"titular"`
	CPFCNPJ     string    `json:"cpf_cnpj"`
}

type itemCache struct {
	entrada  *Entrada
	expiraEm time.Time
	negativo bool
}

type Client struct {
	baseURL     string
	ispb        string
	http        *http.Client
	brk         *breaker.Breaker
	ttl         time.Duration
	ttlNegativo time.Duration

	mu    sync.RWMutex
	cache map[string]itemCache

	hits, misses, bloqueiosLocais int64
}

type Opcoes struct {
	BaseURL       string
	ISPB          string
	Timeout       time.Duration
	TTL           time.Duration
	TTLNegativo   time.Duration
	BreakerFalhas int
	BreakerAberto time.Duration
}

func New(o Opcoes) *Client {
	return &Client{
		baseURL:     strings.TrimRight(o.BaseURL, "/"),
		ispb:        o.ISPB,
		http:        &http.Client{Timeout: o.Timeout},
		brk:         breaker.New(o.BreakerFalhas, o.BreakerAberto),
		ttl:         o.TTL,
		ttlNegativo: o.TTLNegativo,
		cache:       map[string]itemCache{},
	}
}

var (
	ErrNaoEncontrada = errors.New("chave nao encontrada no DICT")
	ErrInvalida      = errors.New("chave fora do formato valido")
	ErrRateLimited   = errors.New("DICT respondeu 429: balde de tokens esgotado")
	ErrIndisponivel  = errors.New("DICT indisponivel")
	ErrTimeout       = errors.New("timeout na consulta ao DICT")
	ErrCircuitoAberto = breaker.ErrCircuitoAberto
)

// Consultar devolve a entrada do DICT. `doCache` diz se a resposta veio do
// cache — em aula, é o número que mostra quantos tokens foram poupados.
func (c *Client) Consultar(chave string) (*Entrada, bool, error) {
	chave = strings.TrimSpace(chave)

	// (1) Validação local: o 404 mais barato é o que nunca sai de casa.
	tipo, err := ClassificarChave(chave)
	if err != nil {
		c.bloqueiosLocais++
		obs.Inc("dict.bloqueado_localmente")
		return nil, false, fmt.Errorf("%w: %s", ErrInvalida, err.Error())
	}
	_ = tipo

	// (2) Cache — positivo e negativo.
	if item, ok := c.doCache(chave); ok {
		c.hits++
		obs.Inc("dict.cache_hit")
		if item.negativo {
			return nil, true, ErrNaoEncontrada
		}
		return item.entrada, true, nil
	}
	c.misses++
	obs.Inc("dict.cache_miss")

	// (3) Rede, protegida por breaker e timeout.
	var entrada *Entrada
	var httpStatus int
	err = c.brk.Executar(func() error {
		e, st, err := c.buscar(chave)
		entrada, httpStatus = e, st
		// 404 é resposta legítima do DICT, não falha da dependência:
		// não pode contar como falha para abrir o circuito.
		if errors.Is(err, ErrNaoEncontrada) {
			return nil
		}
		return err
	})

	if errors.Is(err, breaker.ErrCircuitoAberto) {
		obs.Inc("dict.circuito_aberto")
		return nil, false, ErrCircuitoAberto
	}
	if err != nil {
		return nil, false, err
	}

	if httpStatus == http.StatusNotFound {
		// Cache negativo curto: evita repetir um 404 que custa 20 tokens.
		c.guardar(chave, itemCache{negativo: true, expiraEm: time.Now().Add(c.ttlNegativo)})
		obs.Inc("dict.nao_encontrada")
		return nil, false, ErrNaoEncontrada
	}

	c.guardar(chave, itemCache{entrada: entrada, expiraEm: time.Now().Add(c.ttl)})
	return entrada, false, nil
}

func (c *Client) buscar(chave string) (*Entrada, int, error) {
	url := c.baseURL + "/dict/v1/keys/" + urlEscape(chave)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-ISPB", c.ispb)

	resp, err := c.http.Do(req)
	if err != nil {
		if isTimeout(err) {
			obs.Inc("dict.timeout")
			return nil, 0, fmt.Errorf("%w: %v", ErrTimeout, err)
		}
		return nil, 0, fmt.Errorf("%w: %v", ErrIndisponivel, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var e Entrada
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			return nil, resp.StatusCode, fmt.Errorf("%w: resposta ilegivel", ErrIndisponivel)
		}
		return &e, resp.StatusCode, nil
	case http.StatusNotFound:
		return nil, resp.StatusCode, ErrNaoEncontrada
	case http.StatusTooManyRequests:
		obs.Inc("dict.rate_limited")
		return nil, resp.StatusCode, ErrRateLimited
	default:
		return nil, resp.StatusCode, fmt.Errorf("%w: HTTP %d", ErrIndisponivel, resp.StatusCode)
	}
}

func (c *Client) doCache(chave string) (itemCache, bool) {
	c.mu.RLock()
	item, ok := c.cache[chave]
	c.mu.RUnlock()
	if !ok || time.Now().After(item.expiraEm) {
		return itemCache{}, false
	}
	return item, true
}

func (c *Client) guardar(chave string, item itemCache) {
	c.mu.Lock()
	c.cache[chave] = item
	c.mu.Unlock()
}

func (c *Client) Estado() map[string]any {
	c.mu.RLock()
	tamanho := len(c.cache)
	c.mu.RUnlock()
	return map[string]any{
		"circuito":            c.brk.Snapshot(),
		"cache_entradas":      tamanho,
		"cache_hits":          c.hits,
		"cache_misses":        c.misses,
		"bloqueios_locais":    c.bloqueiosLocais,
		"ttl_positivo_s":      int(c.ttl.Seconds()),
		"ttl_negativo_s":      int(c.ttlNegativo.Seconds()),
		"nota":                "cada miss vira 1 token no DICT; cada 404 custa 20",
	}
}

// ---------------------------------------------------------------------------
// Validação local de chave (economia de token e de latência)
// ---------------------------------------------------------------------------

var (
	reEmail    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[a-zA-Z]{2,}$`)
	reTelefone = regexp.MustCompile(`^\+55[1-9]{2}9?[0-9]{8}$`)
	reEVP      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	reDigitos  = regexp.MustCompile(`^[0-9]+$`)
)

// ClassificarChave descobre o tipo e valida o formato SEM ir à rede.
// Para CPF/CNPJ conferimos até o dígito verificador: uma chave que nem passa
// no módulo 11 jamais existiria no DICT, e consultá-la só queimaria 20 tokens.
func ClassificarChave(chave string) (string, error) {
	switch {
	case chave == "":
		return "", errors.New("chave vazia")
	case reEmail.MatchString(chave):
		return "EMAIL", nil
	case strings.HasPrefix(chave, "+"):
		if !reTelefone.MatchString(chave) {
			return "", errors.New("telefone deve seguir +55DDNNNNNNNNN")
		}
		return "TELEFONE", nil
	case reEVP.MatchString(chave):
		return "EVP", nil
	case reDigitos.MatchString(chave) && len(chave) == 11:
		if !cpfValido(chave) {
			return "", errors.New("CPF com digito verificador invalido")
		}
		return "CPF", nil
	case reDigitos.MatchString(chave) && len(chave) == 14:
		if !cnpjValido(chave) {
			return "", errors.New("CNPJ com digito verificador invalido")
		}
		return "CNPJ", nil
	default:
		return "", errors.New("nao corresponde a CPF, CNPJ, telefone, email ou EVP")
	}
}

func cpfValido(cpf string) bool {
	todosIguais := true
	for i := 1; i < len(cpf); i++ {
		if cpf[i] != cpf[0] {
			todosIguais = false
			break
		}
	}
	if todosIguais {
		return false
	}
	d := make([]int, 11)
	for i := 0; i < 11; i++ {
		d[i] = int(cpf[i] - '0')
	}
	soma := 0
	for i := 0; i < 9; i++ {
		soma += d[i] * (10 - i)
	}
	if d[9] != digitoMod11(soma) {
		return false
	}
	soma = 0
	for i := 0; i < 10; i++ {
		soma += d[i] * (11 - i)
	}
	return d[10] == digitoMod11(soma)
}

func cnpjValido(cnpj string) bool {
	d := make([]int, 14)
	for i := 0; i < 14; i++ {
		d[i] = int(cnpj[i] - '0')
	}
	pesos1 := []int{5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}
	pesos2 := []int{6, 5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}

	soma := 0
	for i, p := range pesos1 {
		soma += d[i] * p
	}
	if d[12] != digitoMod11(soma) {
		return false
	}
	soma = 0
	for i, p := range pesos2 {
		soma += d[i] * p
	}
	return d[13] == digitoMod11(soma)
}

func digitoMod11(soma int) int {
	r := soma % 11
	if r < 2 {
		return 0
	}
	return 11 - r
}

func urlEscape(s string) string {
	r := strings.NewReplacer("+", "%2B", " ", "%20", "#", "%23", "?", "%3F")
	return r.Replace(s)
}

func isTimeout(err error) bool {
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}
