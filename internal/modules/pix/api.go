// Package pix orquestra a "Anatomia de um Pix" da §6.5.
//
//	1. app do pagador  -> TechPix          (ordem: chave + valor)
//	2. TechPix         -> DICT             (resolve a chave; gasta token e latência)
//	3. TechPix                              (validações locais: saldo, limites, PLD-FT)
//	4. TechPix         -> ledger           (RESERVA: D carteira / C pix_a_liquidar)
//	5. TechPix         -> SPI              (pacs.008, com o E2E ID)
//	6. SPI                                  (liquida em moeda de BC: FINAL, irrevogável)
//	7. SPI             -> TechPix          (pacs.002)
//	8. TechPix         -> ledger           (LIQUIDAÇÃO: D pix_a_liquidar / C reserva_no_bc)
//	9. borda assíncrona                     (extrato, saldo projetado, notificação)
//
// Os passos 4 e 8 são os DOIS fatos econômicos do §3.4. Entre eles o dinheiro
// existe num limbo explícito (`pix_a_liquidar`) — e limbo explícito é o que
// torna a falha retomável.
package pix

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Status string

const (
	// Reservado: dinheiro saiu da carteira e está em `pix_a_liquidar`.
	// Se o processo morrer aqui, a reconciliação retoma.
	Reservado Status = "RESERVED"
	Liquidado Status = "SETTLED"
	// Não existe estado "rejeitado" aqui. Quando o SPI rejeita (pacs.002 RJCT),
	// o que importa para o nosso ledger é que o dinheiro VOLTOU: `pix_estorno`
	// e status ESTORNADO. O motivo da rejeição fica em `spi_status`/`spi_reason`.
	Estornado Status = "REFUNDED"
)

type ComandoPagar struct {
	// E2EID nasce no CLIENTE e sobrevive aos retries. É a chave de idempotência.
	// Se o servidor gerasse, cada toque da Ana viraria uma chave nova — e a
	// deduplicação não existiria (§4.3).
	E2EID          string `json:"e2e_id"`
	ContaPagador   string `json:"conta_pagador"`
	ChaveRecebedor string `json:"chave_recebedor"`
	ValorCentavos  int64  `json:"valor_centavos"`
	Descricao      string `json:"descricao,omitempty"`
}

type Pagamento struct {
	E2EID          string    `json:"e2e_id"`
	ContaPagador   string    `json:"conta_pagador"`
	ChaveRecebedor string    `json:"chave_recebedor"`
	ISPBRecebedor  string    `json:"ispb_recebedor"`
	BancoRecebedor string    `json:"banco_recebedor"`
	NomeRecebedor  string    `json:"nome_recebedor"`
	ValorCentavos  int64     `json:"valor_centavos"`
	Valor          string    `json:"valor"`
	Descricao      string    `json:"descricao,omitempty"`
	Status         Status    `json:"status"`
	StatusSPI      string    `json:"status_spi,omitempty"`
	MotivoSPI      string    `json:"motivo_spi,omitempty"`
	TxReserva      string    `json:"tx_reserva,omitempty"`
	TxLiquidacao   string    `json:"tx_liquidacao,omitempty"`
	CriadoEm       time.Time `json:"criado_em"`
	AtualizadoEm   time.Time `json:"atualizado_em"`
}

var (
	ErrE2EInvalido        = errors.New("EndToEndId invalido")
	ErrPagamentoNaoExiste = errors.New("pagamento nao encontrado")
	ErrRecusadoPorRisco   = errors.New("pagamento recusado nas validacoes locais")
)

type Service interface {
	Pagar(ctx context.Context, cmd ComandoPagar) (*Pagamento, *Resposta, error)
	Consultar(ctx context.Context, e2eID string) (*Pagamento, error)
	Listar(ctx context.Context, limite int) ([]Pagamento, error)
}

// Resposta carrega os metadados que a AULA quer ver: se foi replay de uma
// intenção já processada e para onde foi cada milissegundo do orçamento.
type Resposta struct {
	Replay            bool           `json:"replay"`
	StatusHTTP        int            `json:"-"`
	OrcamentoLatencia map[string]any `json:"orcamento_latencia"`
	Observacao        string         `json:"observacao,omitempty"`
	// CorpoBruto é a resposta ORIGINAL guardada na chave de idempotência.
	// Um retry de uma recusa recebe isto, byte a byte — sem reprocessar nada.
	CorpoBruto json.RawMessage `json:"-"`
}
