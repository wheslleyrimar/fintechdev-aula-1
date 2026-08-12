// Package idempotency transforma "a Ana tocou 3 vezes" em "aconteceu 1 vez,
// respondido 3 vezes".
//
// Aula 1 · §4. O ponto de partida é desconfortável: o timeout é AMBÍGUO.
// Quando a resposta não chega, não dá para saber se a operação falhou antes de
// executar ou se executou e a resposta se perdeu. Os dois generais de novo.
//
// Semânticas possíveis:
//   - at-most-once  -> nunca duplica, mas pode PERDER. Inaceitável em fintech.
//   - at-least-once -> nunca perde, mas DUPLICA. É o que a rede real entrega.
//   - exactly-once  -> impossível em rede assíncrona.
//
// O que se consegue de fato é EFEITO exactly-once: a mensagem pode chegar N
// vezes, o efeito no ledger acontece uma só. A ponte é este módulo.
package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/wheslleyrimar/techpix/internal/platform/pg"
)

type Estado string

const (
	EmAndamento Estado = "IN_PROGRESS"
	Concluido   Estado = "DONE"
	Falhou      Estado = "FAILED"
)

// Efeito é o trabalho protegido pela chave. Recebe a transação para que
// "marcar como concluído" e "gravar os lançamentos" sejam ATÔMICOS — ou as
// duas coisas acontecem, ou nenhuma (§4.3, propriedade 3).
type Efeito func(ctx context.Context, tx pg.Tx) (status int, corpo any, err error)

type Resultado struct {
	// Replay = a intenção já tinha sido processada; devolvemos o mesmo resultado.
	Replay      bool            `json:"replay"`
	Status      int             `json:"status"`
	Corpo       json.RawMessage `json:"corpo"`
	Estado      Estado          `json:"estado"`
	RegistradoEm time.Time      `json:"registrado_em"`
}

var (
	// ErrEmAndamento: retry concorrente esperou e o primeiro ainda não terminou.
	// Responder 409 aqui é honesto: não sabemos o desfecho ainda, e chutar é pior.
	ErrEmAndamento = errors.New("operacao com esta chave ainda esta em andamento")
	// ErrChaveReutilizada: mesma chave, payload diferente. Não é retry, é bug
	// do cliente — e responder "ok" seria pagar a coisa errada.
	ErrChaveReutilizada = errors.New("chave de idempotencia reutilizada com payload diferente")
	ErrChaveVazia       = errors.New("chave de idempotencia obrigatoria")
)

// ErroRecusaRegistrada é devolvido quando a chave JÁ carrega uma recusa
// definitiva. O retry não ganha uma segunda chance de passar: ele recebe, de
// volta, exatamente a mesma resposta que a primeira tentativa recebeu.
type ErroRecusaRegistrada struct {
	Status int
	Corpo  json.RawMessage
}

func (e *ErroRecusaRegistrada) Error() string {
	return "esta chave de idempotencia ja tem uma recusa registrada"
}

// ErroRetentavel marca falhas transitórias (rede, indisponibilidade). Nesses
// casos o registro é LIBERADO, para que o cliente possa de fato tentar de novo.
// Erros de negócio (saldo insuficiente) NÃO são retentáveis: ficam gravados e
// o retry recebe a mesma recusa.
type ErroRetentavel struct{ Err error }

func (e *ErroRetentavel) Error() string { return e.Err.Error() }
func (e *ErroRetentavel) Unwrap() error { return e.Err }

func Retentavel(err error) error { return &ErroRetentavel{Err: err} }

type Service interface {
	// Executar roda o efeito no máximo uma vez por chave.
	//
	//   1ª vez            -> executa e persiste o resultado
	//   retry tardio      -> devolve o resultado guardado
	//   retry concorrente -> espera o primeiro terminar e devolve o mesmo resultado
	Executar(ctx context.Context, chave, escopo string, payload any, efeito Efeito) (*Resultado, error)
	Consultar(ctx context.Context, chave string) (*Resultado, error)
}
