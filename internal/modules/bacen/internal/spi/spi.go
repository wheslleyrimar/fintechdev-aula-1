// Package spi fala com o Sistema de Pagamentos Instantâneos.
//
// Aula 1 · §6.3: o SPI liquida CADA Pix individualmente, em tempo real, 24/7,
// em moeda de banco central — e o resultado é irrevogável no instante em que
// acontece. Não existe "desfazer": desfazer é uma NOVA transação (pacs.004).
//
// Consequência para o código: um timeout aqui NÃO autoriza estorno automático
// nem reenvio cego. O desfecho existe, só não chegou até nós. Isso vira
// reconciliação por E2E ID, não adivinhação.
package spi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wheslleyrimar/techpix/internal/platform/obs"
)

type Pacs008 struct {
	E2EID          string `json:"e2e_id"`
	ISPBPagador    string `json:"ispb_pagador"`
	ISPBRecebedor  string `json:"ispb_recebedor"`
	ChaveRecebedor string `json:"chave_recebedor"`
	ValorCentavos  int64  `json:"valor_centavos"`
	Descricao      string `json:"descricao"`
}

type Pacs002 struct {
	E2EID       string    `json:"e2e_id"`
	Status      string    `json:"status"`
	Motivo      string    `json:"motivo,omitempty"`
	LiquidadoEm time.Time `json:"liquidado_em"`
}

var (
	ErrTimeout      = errors.New("timeout no SPI: desfecho da liquidacao desconhecido")
	ErrIndisponivel = errors.New("SPI indisponivel")
	ErrNaoEncontrado = errors.New("E2E ID desconhecido no SPI")
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *Client) Enviar(req Pacs008) (*Pacs002, error) {
	corpo, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	inicio := time.Now()
	resp, err := c.http.Post(c.baseURL+"/spi/v1/pacs008", "application/json", bytes.NewReader(corpo))
	obs.Observe("spi.pacs008", time.Since(inicio))

	if err != nil {
		if isTimeout(err) {
			// O momento mais importante da aula inteira: aqui NÃO SABEMOS se
			// o dinheiro liquidou. Qualquer decisão tomada por chute cria ou
			// destrói dinheiro. O único caminho correto é a reconciliação.
			obs.Inc("spi.timeout")
			return nil, fmt.Errorf("%w: %v", ErrTimeout, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrIndisponivel, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("%w: HTTP %d", ErrIndisponivel, resp.StatusCode)
	}

	var out Pacs002
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: pacs.002 ilegivel", ErrIndisponivel)
	}
	obs.Inc("spi.resposta." + strings.ToLower(out.Status))
	return &out, nil
}

// Consultar é a pergunta que salva a reconciliação: "e o E2E ID X, liquidou?"
func (c *Client) Consultar(e2eID string) (*Pacs002, error) {
	resp, err := c.http.Get(c.baseURL + "/spi/v1/payments/" + e2eID)
	if err != nil {
		if isTimeout(err) {
			return nil, fmt.Errorf("%w: %v", ErrTimeout, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrIndisponivel, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var out Pacs002
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, fmt.Errorf("%w: resposta ilegivel", ErrIndisponivel)
		}
		return &out, nil
	case http.StatusNotFound:
		return nil, ErrNaoEncontrado
	default:
		return nil, fmt.Errorf("%w: HTTP %d", ErrIndisponivel, resp.StatusCode)
	}
}

func isTimeout(err error) bool {
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}
