// Package bacen é a CAMADA ANTICORRUPÇÃO com o Banco Central.
//
// Aula 1 · §6: o BACEN é, ao mesmo tempo, operador (roda SPI e DICT),
// regulador (impõe SLAs e regras) e liquidante final (o dinheiro liquida em
// moeda de banco central). Nada disso está sob nosso controle — e três
// sistemas externos (DICT, SPI e o banco do recebedor) ficam no nosso caminho
// crítico. Projetar Pix é projetar resiliência contra dependência externa.
//
// Este módulo isola tudo que é "do BACEN" atrás de interfaces. Se amanhã o
// DICT mudar de protocolo, muda aqui — o domínio do Pix não fica sabendo.
package bacen

import (
	"errors"
	"time"
)

// ---------------------------------------------------------------------------
// DICT — diretório de chaves (§6.4)
// ---------------------------------------------------------------------------

type ChavePix struct {
	Chave      string    `json:"chave"`
	TipoChave  string    `json:"tipo_chave"`
	ISPB       string    `json:"ispb"`
	Instituicao string   `json:"instituicao"`
	Agencia    string    `json:"agencia"`
	Conta      string    `json:"conta"`
	TipoConta  string    `json:"tipo_conta"`
	Titular    string    `json:"titular"`
	CPFCNPJ    string    `json:"cpf_cnpj"`
	ConsultadoEm time.Time `json:"consultado_em"`
	DoCache    bool      `json:"do_cache"`
}

var (
	// ErrChaveNaoEncontrada: no DICT real, um 404 custa 20 tokens (contra 1 do
	// 200). É punição a scraping — varrer o diretório fica caro de propósito.
	ErrChaveNaoEncontrada = errors.New("chave nao encontrada no DICT")
	// ErrChaveInvalida: pega ANTES de gastar token. Validação local é economia.
	ErrChaveInvalida    = errors.New("chave fora do formato valido")
	ErrDictRateLimited  = errors.New("DICT respondeu 429: balde de tokens esgotado")
	ErrDictIndisponivel = errors.New("DICT indisponivel")
	ErrDictTimeout      = errors.New("timeout na consulta ao DICT")
)

type DICT interface {
	Consultar(chave string) (*ChavePix, error)
	Estado() map[string]any
}

// ---------------------------------------------------------------------------
// SPI — liquidação em moeda de banco central (§6.3)
// ---------------------------------------------------------------------------

type Pacs008 struct {
	E2EID        string `json:"e2e_id"`
	ISPBPagador  string `json:"ispb_pagador"`
	ISPBRecebedor string `json:"ispb_recebedor"`
	ChaveRecebedor string `json:"chave_recebedor"`
	ValorCentavos int64  `json:"valor_centavos"`
	Descricao     string `json:"descricao"`
}

// Pacs002 é a confirmação. ACSC = liquidado (final e irrevogável).
// RJCT = rejeitado.
type Pacs002 struct {
	E2EID       string    `json:"e2e_id"`
	Status      string    `json:"status"`
	Motivo      string    `json:"motivo,omitempty"`
	LiquidadoEm time.Time `json:"liquidado_em"`
}

const (
	StatusLiquidado = "ACSC"
	StatusRejeitado = "RJCT"
	StatusEmAnalise = "PDNG"
)

var (
	// ErrSPITimeout é O erro central da aula: NÃO sabemos se liquidou.
	// Não pode virar estorno automático nem reenvio cego. Vira reconciliação.
	ErrSPITimeout       = errors.New("timeout no SPI: desfecho da liquidacao desconhecido")
	ErrSPIIndisponivel  = errors.New("SPI indisponivel")
	ErrSPINaoEncontrado = errors.New("E2E ID desconhecido no SPI")
)

type SPI interface {
	// Enviar manda a instrução de pagamento (pacs.008) e espera o pacs.002.
	Enviar(req Pacs008) (*Pacs002, error)
	// ConsultarStatus pergunta ao SPI o desfecho de um E2E ID. É a base da
	// reconciliação: quando a resposta se perde, a verdade ainda existe lá.
	ConsultarStatus(e2eID string) (*Pacs002, error)
}
