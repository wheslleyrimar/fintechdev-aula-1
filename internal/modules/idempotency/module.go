package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/wheslleyrimar/techpix/internal/modules/idempotency/internal/store"
	"github.com/wheslleyrimar/techpix/internal/platform/httpx"
	"github.com/wheslleyrimar/techpix/internal/platform/obs"
	"github.com/wheslleyrimar/techpix/internal/platform/pg"
)

type Module struct {
	db       *pg.DB
	espera   time.Duration // quanto um retry concorrente aguarda o primeiro
	lockTTL  time.Duration // depois de quanto tempo um IN_PROGRESS é considerado órfão
}

var _ Service = (*Module)(nil)

func New(db *pg.DB, espera, lockTTL time.Duration) *Module {
	return &Module{db: db, espera: espera, lockTTL: lockTTL}
}

func (m *Module) Nome() string { return "idempotency" }

func (m *Module) Rotas(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/idempotencia/{chave}", m.handleConsultar)
}

// Executar implementa o desenho da §4.3.
func (m *Module) Executar(ctx context.Context, chave, escopo string, payload any, efeito Efeito) (*Resultado, error) {
	if chave == "" {
		return nil, ErrChaveVazia
	}
	hash := hashPayload(payload)

	limite := time.Now().Add(m.espera)
	for {
		// (1) Tentar virar dono da chave. Atômico: quem insere, executa.
		dono, err := store.Reivindicar(ctx, m.db.Pool, chave, escopo, hash)
		if err != nil {
			return nil, err
		}
		if dono {
			obs.Inc("idempotencia.primeira_execucao")
			return m.executarEfeito(ctx, chave, hash, efeito)
		}

		// (2) Alguém já registrou esta intenção. Ver em que pé está.
		reg, err := store.Buscar(ctx, m.db.Pool, chave)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue // corrida com um Liberar(): tenta reivindicar de novo
			}
			return nil, err
		}

		if reg.Hash != hash {
			// Mesma chave, intenção diferente. Isso não é retry.
			obs.Inc("idempotencia.chave_reutilizada")
			return nil, ErrChaveReutilizada
		}

		switch Estado(reg.Estado) {
		case Concluido, Falhou:
			// Retry tardio: devolve o resultado guardado. Zero efeito novo.
			obs.Inc("idempotencia.replay")
			res := &Resultado{
				Replay: true, Status: reg.Status, Corpo: reg.Corpo,
				Estado: Estado(reg.Estado), RegistradoEm: reg.CriadoEm,
			}
			if Estado(reg.Estado) == Falhou {
				// Recusa é desfecho, não ausência de desfecho: o chamador
				// precisa tratá-la como recusa, e não como sucesso silencioso.
				return res, &ErroRecusaRegistrada{Status: reg.Status, Corpo: reg.Corpo}
			}
			return res, nil

		case EmAndamento:
			// Retry CONCORRENTE: o primeiro ainda está executando.
			// Registro órfão (processo morreu)? Reassume e executa.
			if reassumiu, err := store.Reassumir(ctx, m.db.Pool, chave, m.lockTTL); err != nil {
				return nil, err
			} else if reassumiu {
				slog.Warn("chave de idempotencia orfa reassumida", "chave", chave)
				obs.Inc("idempotencia.orfa_reassumida")
				return m.executarEfeito(ctx, chave, hash, efeito)
			}

			if time.Now().After(limite) {
				obs.Inc("idempotencia.em_andamento_timeout")
				return nil, ErrEmAndamento
			}
			obs.Inc("idempotencia.espera_concorrente")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
}

// executarEfeito roda o trabalho protegido numa transação SERIALIZABLE.
//
// No caminho de SUCESSO, "marcar concluído" e "gravar os lançamentos" vão no
// MESMO commit — é a propriedade 3 da §4.3, e é o que impede que exista um
// débito sem registro de idempotência (ou vice-versa).
//
// No caminho de RECUSA, a transação é DESFEITA por inteiro: um efeito que
// falhou no meio (inseriu lançamentos e só então percebeu que o saldo ficaria
// negativo) não pode deixar rastro. Só depois do rollback é que gravamos o
// desfecho "FAILED", numa transação própria — aí não há lançamento nenhum para
// manter atômico, apenas a decisão.
func (m *Module) executarEfeito(ctx context.Context, chave, hash string, efeito Efeito) (*Resultado, error) {
	var (
		status    int
		corpoJSON []byte
		statusRecusa int
	)

	efeitoErr := m.db.InSerializableTx(ctx, func(tx pg.Tx) error {
		st, corpo, err := efeito(ctx, tx)
		if err != nil {
			statusRecusa = st
			return err // SEMPRE aborta: nada meio-gravado sobrevive
		}
		status = st
		corpoJSON = mustJSON(corpo)
		return store.Concluir(ctx, tx, chave, string(Concluido), status, corpoJSON)
	})

	if efeitoErr == nil {
		return &Resultado{Status: status, Corpo: corpoJSON, Estado: Concluido, RegistradoEm: time.Now()}, nil
	}

	// Falha transitória: libera a chave para que o cliente possa DE FATO
	// tentar de novo. Um retry com a mesma chave voltará a executar.
	var retentavel *ErroRetentavel
	if errors.As(efeitoErr, &retentavel) || ehFalhaDeInfra(efeitoErr) {
		if delErr := store.Liberar(ctx, m.db.Pool, chave); delErr != nil {
			slog.Error("falha ao liberar chave de idempotencia", "chave", chave, "erro", delErr)
		}
		obs.Inc("idempotencia.liberada_para_retry")
		return nil, efeitoErr
	}

	// Recusa de negócio: desfecho DEFINITIVO. Fica gravado para que qualquer
	// retry receba exatamente a mesma recusa, e não uma segunda chance.
	if statusRecusa == 0 {
		statusRecusa = http.StatusUnprocessableEntity
	}
	corpoJSON = mustJSON(map[string]any{
		"erro": map[string]any{"codigo": "RECUSADO", "mensagem": efeitoErr.Error()},
	})
	if err := store.Concluir(ctx, m.db.Pool, chave, string(Falhou), statusRecusa, corpoJSON); err != nil {
		slog.Error("falha ao gravar recusa idempotente", "chave", chave, "erro", err)
	}
	obs.Inc("idempotencia.recusa_registrada")

	return &Resultado{Status: statusRecusa, Corpo: corpoJSON, Estado: Falhou, RegistradoEm: time.Now()}, efeitoErr
}

// ehFalhaDeInfra distingue "o banco caiu" de "o cliente não tem saldo".
// O primeiro merece nova tentativa; o segundo, não.
func ehFalhaDeInfra(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		pg.IsSerializationFailure(err)
}

func (m *Module) Consultar(ctx context.Context, chave string) (*Resultado, error) {
	reg, err := store.Buscar(ctx, m.db.Pool, chave)
	if err != nil {
		return nil, err
	}
	return &Resultado{
		Replay: true, Status: reg.Status, Corpo: reg.Corpo,
		Estado: Estado(reg.Estado), RegistradoEm: reg.CriadoEm,
	}, nil
}

func (m *Module) handleConsultar(w http.ResponseWriter, r *http.Request) {
	res, err := m.Consultar(r.Context(), r.PathValue("chave"))
	if err != nil {
		httpx.Fail(w, http.StatusNotFound, "CHAVE_NAO_ENCONTRADA", "nenhuma intencao registrada com essa chave", nil)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// hashPayload dá a "impressão digital" da INTENÇÃO. Mesma chave + payload
// diferente = erro, não replay.
func hashPayload(payload any) string {
	b, err := json.Marshal(payload)
	if err != nil {
		return "erro:" + err.Error()
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(fmt.Sprintf(`{"erro":%q}`, err.Error()))
	}
	return b
}
