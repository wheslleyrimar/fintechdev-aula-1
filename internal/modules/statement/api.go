// Package statement é a BORDA: o read model.
//
// Aula 1 · §5.5, a linha que resolve 80% da arquitetura de dados de uma fintech:
//
//	ledger, núcleo          -> consistência FORTE, linearizável
//	extrato, feed, saldo    -> consistência EVENTUAL (100–300ms de atraso)
//
// E o §3.5 explica por que isso não é luxo: leitura >> escrita, então separar
// os modelos (CQRS) é necessidade.
//
// O ponto pedagógico do módulo: este saldo aqui pode estar DESATUALIZADO — de
// propósito. Ver o extrato com 250ms de atraso não machuca ninguém. Debitar
// errado, sim. Por isso quem autoriza pagamento é o ledger, nunca esta tabela.
package statement

import (
	"context"
	"time"

	"github.com/wheslleyrimar/techpix/internal/platform/money"
)

type SaldoProjetado struct {
	ContaCodigo   string      `json:"conta"`
	SaldoCentavos money.Cents `json:"saldo_centavos"`
	Saldo         string      `json:"saldo"`
	UltimoLancamento int64    `json:"ultimo_lancamento_projetado"`
	AtualizadoEm  time.Time   `json:"atualizado_em"`
	AtrasoMs      float64     `json:"atraso_ms"`
}

type LinhaExtrato struct {
	LancamentoID  int64       `json:"lancamento_id"`
	E2EID         string      `json:"e2e_id,omitempty"`
	Tipo          string      `json:"tipo"`
	Descricao     string      `json:"descricao"`
	Direcao       string      `json:"direcao"`
	ValorCentavos money.Cents `json:"valor_centavos"`
	Valor         string      `json:"valor"`
	SaldoApos     string      `json:"saldo_apos"`
	OcorridoEm    time.Time   `json:"ocorrido_em"`
	ProjetadoEm   time.Time   `json:"projetado_em"`
}

type Service interface {
	SaldoEventual(ctx context.Context, conta string) (*SaldoProjetado, error)
	Extrato(ctx context.Context, conta string, limite int) ([]LinhaExtrato, error)
	Defasagem(ctx context.Context) (lancamentosAtrasados int64, err error)
}
