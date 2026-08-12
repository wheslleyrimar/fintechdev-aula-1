// Package risco são as validações locais do passo 3 da §6.5:
// saldo, limites (inclusive limite noturno), antifraude e PLD-FT.
//
// Uma regra atravessa tudo aqui: FALHAR FECHADO. Na dúvida, recusa.
// "Melhor recusar uma operação que debitar errado" — a propriedade
// "correção > disponibilidade" do núcleo, virada código.
package risco

import (
	"errors"
	"fmt"
	"time"
)

// Horário de Brasília sem depender de tzdata no container.
var brasilia = time.FixedZone("BRT", -3*60*60)

type Motivo struct {
	Codigo   string
	Mensagem string
}

func (m *Motivo) Error() string { return m.Codigo + ": " + m.Mensagem }

var ErrRecusado = errors.New("recusado nas validacoes locais")

type Politica struct {
	ValorMaximoCentavos int64
	LimiteNoturnoCentavos int64
	Blocklist           []string
}

type Pedido struct {
	ValorCentavos int64
	ContaPagador  string
	ChaveRecebedor string
	CPFCNPJRecebedor string
	Agora         time.Time
}

// Avaliar devolve nil se o pagamento pode seguir. Qualquer recusa é definitiva
// e vira resposta gravada na chave de idempotência: um retry recebe a MESMA
// recusa, e não uma segunda chance de passar.
func (p Politica) Avaliar(req Pedido) error {
	if req.ValorCentavos <= 0 {
		return &Motivo{"VALOR_INVALIDO", "o valor deve ser positivo"}
	}
	if req.ValorCentavos > p.ValorMaximoCentavos {
		return &Motivo{"LIMITE_EXCEDIDO",
			fmt.Sprintf("valor acima do limite por transacao (%d centavos)", p.ValorMaximoCentavos)}
	}

	// Limite noturno (20h–06h): regra que existe no Pix real para reduzir
	// sequestro relâmpago. É risco virando parâmetro de arquitetura.
	hora := req.Agora.In(brasilia).Hour()
	if (hora >= 20 || hora < 6) && req.ValorCentavos > p.LimiteNoturnoCentavos {
		return &Motivo{"LIMITE_NOTURNO",
			fmt.Sprintf("entre 20h e 6h o limite e de %d centavos", p.LimiteNoturnoCentavos)}
	}

	// PLD-FT: Prevenção à Lavagem de Dinheiro e ao Financiamento do Terrorismo.
	for _, doc := range p.Blocklist {
		if doc != "" && doc == req.CPFCNPJRecebedor {
			return &Motivo{"PLDFT_BLOQUEADO", "recebedor em lista restritiva"}
		}
	}

	return nil
}
