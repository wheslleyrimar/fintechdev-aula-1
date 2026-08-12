// Package ledger é o NÚCLEO. Consistência forte, linearizável, síncrona no
// caminho crítico (ADR-001).
//
// Regra de ouro do módulo, que vale para o curso inteiro:
//
//	NUNCA "atualizar saldo". SEMPRE "registrar movimento".
//
// Só existe uma porta de entrada para o dinheiro se mover: Registrar/RegistrarTx.
// Nenhum outro módulo escreve em `entries` — e o compilador garante isso,
// porque as queries vivem em ledger/internal/store.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wheslleyrimar/techpix/internal/platform/money"
	"github.com/wheslleyrimar/techpix/internal/platform/pg"
)

type Direcao string

const (
	Debito  Direcao = "D"
	Credito Direcao = "C"
)

// Lancamento é um débito OU um crédito numa conta. Nunca existe sozinho.
type Lancamento struct {
	Conta   string      `json:"conta"`
	Direcao Direcao     `json:"direcao"`
	Valor   money.Cents `json:"valor_centavos"`
}

// PedidoTransacao é um fato econômico: um conjunto BALANCEADO de lançamentos.
type PedidoTransacao struct {
	E2EID       string
	Tipo        string
	Descricao   string
	Lancamentos []Lancamento
}

type LancamentoView struct {
	ID      int64       `json:"id"`
	Conta   string      `json:"conta"`
	Direcao Direcao     `json:"direcao"`
	Valor   money.Cents `json:"valor_centavos"`
	ValorBR string      `json:"valor"`
}

type Transacao struct {
	ID          string           `json:"id"`
	E2EID       string           `json:"e2e_id,omitempty"`
	Tipo        string           `json:"tipo"`
	Descricao   string           `json:"descricao"`
	OcorridoEm  time.Time        `json:"ocorrido_em"`
	Lancamentos []LancamentoView `json:"lancamentos"`
}

type Conta struct {
	ID            string      `json:"id"`
	Codigo        string      `json:"codigo"`
	Nome          string      `json:"nome"`
	Natureza      string      `json:"natureza"`
	PermiteNeg    bool        `json:"permite_negativo"`
	Titular       string      `json:"titular,omitempty"`
	SaldoCentavos money.Cents `json:"saldo_centavos"`
	Saldo         string      `json:"saldo"`
}

// Erros de domínio. Cada um deles é uma invariante da Aula 1 se defendendo.
var (
	// Σ débitos ≠ Σ créditos. Dinheiro sendo criado ou destruído.
	ErrDesbalanceada = errors.New("transacao desbalanceada: soma dos debitos difere da soma dos creditos")
	// Conservação: não se paga o que não se tem.
	ErrSaldoInsuficiente = errors.New("saldo insuficiente")
	ErrContaDesconhecida = errors.New("conta contabil desconhecida")
	// Mesmo E2E ID, mesmo tipo: o BACEN proíbe. O banco também.
	ErrDuplicada = errors.New("transacao duplicada para o mesmo EndToEndId")
	ErrPedidoInvalido = errors.New("pedido de transacao invalido")
)

type ErroSaldo struct {
	Conta      string
	Saldo      money.Cents
	Necessario money.Cents
}

func (e *ErroSaldo) Error() string {
	return fmt.Sprintf("saldo insuficiente em %s: saldo %s, necessario %s",
		e.Conta, e.Saldo.BRL(), e.Necessario.BRL())
}
func (e *ErroSaldo) Unwrap() error { return ErrSaldoInsuficiente }

// Relatorio é o Harness (§7.4) em forma de dado: as invariantes viram teste.
type Relatorio struct {
	Aprovado bool           `json:"aprovado"`
	Checks   []Check        `json:"checks"`
	Resumo   map[string]any `json:"resumo"`
}

type Check struct {
	Nome      string `json:"nome"`
	Invariante string `json:"invariante"`
	Aprovado  bool   `json:"aprovado"`
	Detalhe   string `json:"detalhe"`
}

// Service é o contrato público do módulo. Outros módulos só enxergam isto.
type Service interface {
	// RegistrarTx participa da transação do chamador. Existe porque o efeito
	// no ledger e o registro de idempotência precisam ser atômicos (§4.3).
	RegistrarTx(ctx context.Context, tx pg.Tx, p PedidoTransacao) (*Transacao, error)
	// Registrar abre a própria transação SERIALIZABLE (com retry).
	Registrar(ctx context.Context, p PedidoTransacao) (*Transacao, error)

	// SaldoForte lê do log, dentro do núcleo. É o saldo que autoriza pagamento.
	SaldoForte(ctx context.Context, conta string) (money.Cents, error)
	SaldoForteTx(ctx context.Context, tx pg.Tx, conta string) (money.Cents, error)

	Conta(ctx context.Context, codigo string) (*Conta, error)
	Contas(ctx context.Context) ([]Conta, error)
	Transacao(ctx context.Context, id string) (*Transacao, error)
	TransacoesPorE2E(ctx context.Context, e2e string) ([]Transacao, error)

	Fitness(ctx context.Context) (*Relatorio, error)
}
