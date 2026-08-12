// Package tests é o HARNESS da Aula 1 (§7.4).
//
// "Uma boa especificação codifica invariantes do domínio — e essas invariantes
// viram testes automáticos (fitness functions)."
//
// Cada teste aqui é uma frase da aula virada guardrail executável:
//
//	Σ débitos = Σ créditos
//	saldo de cliente nunca fica negativo
//	lançamento gravado nunca muda
//	EndToEndId é único
//	três toques da Ana = um débito só
//
// São eles que tornam aceitável deixar um agente propor mudanças no sistema:
// nenhuma proposta escapa do guardrail.
package tests

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/wheslleyrimar/techpix/internal/modules/idempotency"
	"github.com/wheslleyrimar/techpix/internal/modules/ledger"
	"github.com/wheslleyrimar/techpix/internal/platform/ids"
	"github.com/wheslleyrimar/techpix/internal/platform/money"
	"github.com/wheslleyrimar/techpix/internal/platform/pg"
	"github.com/wheslleyrimar/techpix/migrations"
)

var db *pg.DB

func TestMain(m *testing.M) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		fmt.Println("TEST_DATABASE_URL nao definido — rode `make test`")
		os.Exit(0)
	}

	ctx := context.Background()
	if err := criarBancoDeTeste(ctx, url); err != nil {
		fmt.Println("falha ao preparar banco de teste:", err)
		os.Exit(1)
	}

	var err error
	db, err = pg.Open(ctx, url, 30, 8)
	if err != nil {
		fmt.Println("falha ao conectar:", err)
		os.Exit(1)
	}
	if err := db.Migrate(ctx, migrations.FS); err != nil {
		fmt.Println("falha nas migrations:", err)
		os.Exit(1)
	}

	code := m.Run()
	db.Close()
	os.Exit(code)
}

func criarBancoDeTeste(ctx context.Context, url string) error {
	admin := os.Getenv("ADMIN_DATABASE_URL")
	if admin == "" {
		return nil
	}
	nome := nomeDoBanco(url)
	if nome == "" {
		return nil
	}

	pool, err := pgxpool.New(ctx, admin)
	if err != nil {
		return err
	}
	defer pool.Close()

	for i := 0; i < 30; i++ {
		if err = pool.Ping(ctx); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return err
	}

	_, err = pool.Exec(ctx, "CREATE DATABASE "+nome)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	return nil
}

func nomeDoBanco(url string) string {
	i := strings.LastIndex(url, "/")
	if i < 0 {
		return ""
	}
	resto := url[i+1:]
	if j := strings.Index(resto, "?"); j >= 0 {
		resto = resto[:j]
	}
	return resto
}

// ---------------------------------------------------------------------------
// Cenário isolado por teste
// ---------------------------------------------------------------------------

type cenario struct {
	t        *testing.T
	sufixo   string
	ledger   *ledger.Module
	idem     *idempotency.Module
	Carteira string
	Destino  string
	Origem   string
}

func novoCenario(t *testing.T, saldoInicial money.Cents) *cenario {
	t.Helper()
	ctx := context.Background()
	sufixo := strings.ReplaceAll(ids.NewUUID()[:8], "-", "")

	c := &cenario{
		t:        t,
		sufixo:   sufixo,
		ledger:   ledger.New(db, "optimistic"),
		idem:     idempotency.New(db, 3*time.Second, 30*time.Second),
		Carteira: "teste:carteira:" + sufixo,
		Destino:  "teste:aliquidar:" + sufixo,
		Origem:   "teste:funding:" + sufixo,
	}

	criarConta(t, c.Carteira, "PASSIVO", false)
	criarConta(t, c.Destino, "PASSIVO", false)
	criarConta(t, c.Origem, "PASSIVO", true) // funding pode ficar negativa

	if saldoInicial > 0 {
		if _, err := c.ledger.Registrar(ctx, ledger.PedidoTransacao{
			Tipo:      "deposito_teste",
			Descricao: "saldo inicial do cenario",
			Lancamentos: []ledger.Lancamento{
				{Conta: c.Origem, Direcao: ledger.Debito, Valor: saldoInicial},
				{Conta: c.Carteira, Direcao: ledger.Credito, Valor: saldoInicial},
			},
		}); err != nil {
			t.Fatalf("falha ao dar saldo inicial: %v", err)
		}
	}
	return c
}

func criarConta(t *testing.T, codigo, natureza string, permiteNegativo bool) {
	t.Helper()
	_, err := db.Pool.Exec(context.Background(),
		`INSERT INTO accounts (id, code, name, kind, allow_negative) VALUES ($1::uuid, $2, $2, $3, $4)`,
		ids.NewUUID(), codigo, natureza, permiteNegativo)
	if err != nil {
		t.Fatalf("falha ao criar conta %s: %v", codigo, err)
	}
}

// ---------------------------------------------------------------------------
// FITNESS #1 — Σ débitos = Σ créditos
// ---------------------------------------------------------------------------

func TestPartidaDobradaBloqueiaTransacaoDesbalanceada(t *testing.T) {
	c := novoCenario(t, 10_000)

	_, err := c.ledger.Registrar(context.Background(), ledger.PedidoTransacao{
		Tipo: "tentativa_de_criar_dinheiro",
		Lancamentos: []ledger.Lancamento{
			{Conta: c.Carteira, Direcao: ledger.Debito, Valor: 100},
			{Conta: c.Destino, Direcao: ledger.Credito, Valor: 900}, // dinheiro do nada
		},
	})

	if !errors.Is(err, ledger.ErrDesbalanceada) {
		t.Fatalf("dinheiro foi criado: esperava ErrDesbalanceada, veio %v", err)
	}
}

// O guardrail também precisa existir no BANCO, não só no código: um INSERT
// direto, por fora da aplicação, tem que falhar do mesmo jeito.
func TestPartidaDobradaEhImpostaPeloBanco(t *testing.T) {
	c := novoCenario(t, 10_000)
	ctx := context.Background()

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txID := ids.NewUUID()
	if _, err := tx.Exec(ctx,
		`INSERT INTO transactions (id, kind, description) VALUES ($1::uuid, 'burla', 'insert manual')`, txID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO entries (transaction_id, account_id, direction, amount_cents)
		SELECT $1::uuid, id, 'C', 500000 FROM accounts WHERE code = $2`, txID, c.Carteira); err != nil {
		t.Fatal(err)
	}

	// O crédito solto só é cobrado no COMMIT (constraint trigger DEFERRED).
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("o banco aceitou um credito sem contrapartida: partida dobrada nao esta protegida")
	}
}

// ---------------------------------------------------------------------------
// FITNESS #2 — saldo de cliente nunca fica negativo
// ---------------------------------------------------------------------------

func TestSaldoNuncaFicaNegativo(t *testing.T) {
	c := novoCenario(t, 10_000) // R$ 100,00

	_, err := c.ledger.Registrar(context.Background(), ledger.PedidoTransacao{
		Tipo: "pix_reserva",
		Lancamentos: []ledger.Lancamento{
			{Conta: c.Carteira, Direcao: ledger.Debito, Valor: 15_000},
			{Conta: c.Destino, Direcao: ledger.Credito, Valor: 15_000},
		},
	})
	if !errors.Is(err, ledger.ErrSaldoInsuficiente) {
		t.Fatalf("esperava recusa por saldo, veio %v", err)
	}

	saldo, err := c.ledger.SaldoForte(context.Background(), c.Carteira)
	if err != nil {
		t.Fatal(err)
	}
	if saldo != 10_000 {
		t.Fatalf("a tentativa recusada deixou rastro: saldo %s", saldo.BRL())
	}
}

// Concorrência é onde a teoria encontra a realidade: 10 débitos simultâneos de
// R$ 30 sobre um saldo de R$ 100. Exatamente 3 podem passar. Nem 4 (dinheiro
// criado), nem 2 (recusa indevida). É o SERIALIZABLE fazendo o trabalho.
func TestConcorrenciaNaoDeixaSaldoEstourar(t *testing.T) {
	c := novoCenario(t, 10_000)
	const valor = money.Cents(3_000)
	const tentativas = 10

	var wg sync.WaitGroup
	sucessos := make(chan bool, tentativas)

	for i := 0; i < tentativas; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Sob contenção alta, o SSI aborta transações — e retentar faz
			// parte do contrato (§3.7). O que NÃO pode acontecer é o saldo
			// estourar, aconteça quantas retentativas acontecerem.
			for tentativa := 0; tentativa < 25; tentativa++ {
				_, err := c.ledger.Registrar(context.Background(), ledger.PedidoTransacao{
					Tipo: "pix_reserva",
					Lancamentos: []ledger.Lancamento{
						{Conta: c.Carteira, Direcao: ledger.Debito, Valor: valor},
						{Conta: c.Destino, Direcao: ledger.Credito, Valor: valor},
					},
				})
				switch {
				case err == nil:
					sucessos <- true
					return
				case errors.Is(err, ledger.ErrSaldoInsuficiente):
					sucessos <- false
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			sucessos <- false
		}()
	}
	wg.Wait()
	close(sucessos)

	aprovados := 0
	for ok := range sucessos {
		if ok {
			aprovados++
		}
	}

	if aprovados != 3 {
		t.Fatalf("exatamente 3 debitos de R$30 cabem em R$100; passaram %d", aprovados)
	}

	saldo, err := c.ledger.SaldoForte(context.Background(), c.Carteira)
	if err != nil {
		t.Fatal(err)
	}
	if saldo != 1_000 {
		t.Fatalf("saldo final deveria ser R$ 10,00, veio %s", saldo.BRL())
	}
}

// ---------------------------------------------------------------------------
// FITNESS #3 — o log é imutável
// ---------------------------------------------------------------------------

func TestLogEhAppendOnly(t *testing.T) {
	c := novoCenario(t, 5_000)
	ctx := context.Background()

	if _, err := db.Pool.Exec(ctx, `
		UPDATE entries SET amount_cents = 1
		 WHERE account_id = (SELECT id FROM accounts WHERE code = $1)`, c.Carteira); err == nil {
		t.Fatal("UPDATE em entries foi aceito: o log nao e imutavel")
	}

	if _, err := db.Pool.Exec(ctx, `
		DELETE FROM entries
		 WHERE account_id = (SELECT id FROM accounts WHERE code = $1)`, c.Carteira); err == nil {
		t.Fatal("DELETE em entries foi aceito: o log nao e imutavel")
	}
}

// ---------------------------------------------------------------------------
// FITNESS #4 — EndToEndId é único (regra do BACEN virada constraint)
// ---------------------------------------------------------------------------

func TestE2EIDNaoSeRepetePorTipo(t *testing.T) {
	c := novoCenario(t, 50_000)
	ctx := context.Background()
	e2e := ids.NewE2EID("00000001", time.Now())

	pedido := ledger.PedidoTransacao{
		E2EID: e2e,
		Tipo:  "pix_reserva",
		Lancamentos: []ledger.Lancamento{
			{Conta: c.Carteira, Direcao: ledger.Debito, Valor: 1_000},
			{Conta: c.Destino, Direcao: ledger.Credito, Valor: 1_000},
		},
	}

	if _, err := c.ledger.Registrar(ctx, pedido); err != nil {
		t.Fatalf("primeira reserva deveria passar: %v", err)
	}
	if _, err := c.ledger.Registrar(ctx, pedido); !errors.Is(err, ledger.ErrDuplicada) {
		t.Fatalf("segunda reserva com o mesmo E2E ID deveria ser bloqueada, veio %v", err)
	}

	// Já a LIQUIDAÇÃO do mesmo Pix usa o mesmo E2E ID com outro tipo: permitido.
	pedido.Tipo = "pix_liquidacao"
	if _, err := c.ledger.Registrar(ctx, pedido); err != nil {
		t.Fatalf("liquidacao do mesmo E2E ID deveria ser permitida: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FITNESS #5 — os três toques da Ana
// ---------------------------------------------------------------------------

// O teste que dá nome à aula: a Ana toca "pagar" três vezes, com a MESMA
// chave. O efeito no ledger acontece uma vez só; as outras duas respostas são
// replays da primeira.
func TestTresToquesDaAnaGeramUmDebitoSo(t *testing.T) {
	c := novoCenario(t, 100_000) // R$ 1.000,00
	ctx := context.Background()

	e2e := ids.NewE2EID("00000001", time.Now())
	cmd := map[string]any{"conta": c.Carteira, "valor_centavos": 500_00}

	const toques = 3
	var wg sync.WaitGroup
	resultados := make(chan *idempotency.Resultado, toques)
	erros := make(chan error, toques)

	for i := 0; i < toques; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := c.idem.Executar(ctx, e2e, "pix.pagamento", cmd,
				func(ctx context.Context, tx pg.Tx) (int, any, error) {
					tr, err := c.ledger.RegistrarTx(ctx, tx, ledger.PedidoTransacao{
						E2EID: e2e,
						Tipo:  "pix_reserva",
						Lancamentos: []ledger.Lancamento{
							{Conta: c.Carteira, Direcao: ledger.Debito, Valor: 50_000},
							{Conta: c.Destino, Direcao: ledger.Credito, Valor: 50_000},
						},
					})
					if err != nil {
						return 422, nil, err
					}
					return 201, tr, nil
				})
			if err != nil {
				erros <- err
				return
			}
			resultados <- res
		}()
	}
	wg.Wait()
	close(resultados)
	close(erros)

	for err := range erros {
		t.Fatalf("nenhum dos tres toques deveria falhar: %v", err)
	}

	execucoes, replays := 0, 0
	for res := range resultados {
		if res.Replay {
			replays++
		} else {
			execucoes++
		}
	}

	if execucoes != 1 || replays != 2 {
		t.Fatalf("esperava 1 execucao e 2 replays; veio %d e %d", execucoes, replays)
	}

	// A prova que interessa: o dinheiro se moveu UMA vez.
	saldo, err := c.ledger.SaldoForte(ctx, c.Carteira)
	if err != nil {
		t.Fatal(err)
	}
	if saldo != 50_000 {
		t.Fatalf("a Ana foi cobrada mais de uma vez: saldo %s (esperado R$ 500,00)", saldo.BRL())
	}

	var lancamentos int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM entries e JOIN transactions t ON t.id = e.transaction_id WHERE t.e2e_id = $1`,
		e2e).Scan(&lancamentos); err != nil {
		t.Fatal(err)
	}
	if lancamentos != 2 {
		t.Fatalf("esperava 2 lancamentos (um par balanceado), vieram %d", lancamentos)
	}
}

// Mesma chave com OUTRO payload não é retry: é bug do cliente. Aceitar seria
// pagar a coisa errada com a bênção da idempotência.
func TestMesmaChaveComPayloadDiferenteEhRecusada(t *testing.T) {
	c := novoCenario(t, 100_000)
	ctx := context.Background()
	chave := ids.NewE2EID("00000001", time.Now())

	efeito := func(valor money.Cents) idempotency.Efeito {
		return func(ctx context.Context, tx pg.Tx) (int, any, error) {
			tr, err := c.ledger.RegistrarTx(ctx, tx, ledger.PedidoTransacao{
				Tipo: "pix_reserva",
				Lancamentos: []ledger.Lancamento{
					{Conta: c.Carteira, Direcao: ledger.Debito, Valor: valor},
					{Conta: c.Destino, Direcao: ledger.Credito, Valor: valor},
				},
			})
			if err != nil {
				return 422, nil, err
			}
			return 201, tr, nil
		}
	}

	if _, err := c.idem.Executar(ctx, chave, "pix.pagamento", map[string]any{"valor": 100}, efeito(100)); err != nil {
		t.Fatalf("primeira execucao deveria passar: %v", err)
	}
	_, err := c.idem.Executar(ctx, chave, "pix.pagamento", map[string]any{"valor": 999}, efeito(999))
	if !errors.Is(err, idempotency.ErrChaveReutilizada) {
		t.Fatalf("esperava ErrChaveReutilizada, veio %v", err)
	}
}

// Uma recusa é desfecho DEFINITIVO: o retry recebe a mesma recusa, e não uma
// segunda chance de passar. E não pode ter sobrado nenhum lançamento parcial.
func TestRecusaEhIdempotenteENaoDeixaRastro(t *testing.T) {
	c := novoCenario(t, 1_000) // R$ 10,00
	ctx := context.Background()
	chave := ids.NewE2EID("00000001", time.Now())

	efeito := func(ctx context.Context, tx pg.Tx) (int, any, error) {
		tr, err := c.ledger.RegistrarTx(ctx, tx, ledger.PedidoTransacao{
			Tipo: "pix_reserva",
			Lancamentos: []ledger.Lancamento{
				{Conta: c.Carteira, Direcao: ledger.Debito, Valor: 90_000},
				{Conta: c.Destino, Direcao: ledger.Credito, Valor: 90_000},
			},
		})
		if err != nil {
			return 422, nil, err
		}
		return 201, tr, nil
	}

	payload := map[string]any{"valor": 90_000}
	if _, err := c.idem.Executar(ctx, chave, "pix.pagamento", payload, efeito); !errors.Is(err, ledger.ErrSaldoInsuficiente) {
		t.Fatalf("esperava recusa por saldo, veio %v", err)
	}

	res, err := c.idem.Executar(ctx, chave, "pix.pagamento", payload, efeito)
	if err == nil {
		t.Fatal("o retry de uma recusa nao pode virar sucesso")
	}
	if res == nil || !res.Replay || res.Estado != idempotency.Falhou {
		t.Fatalf("o retry deveria devolver a MESMA recusa gravada, veio %+v", res)
	}

	saldo, err := c.ledger.SaldoForte(ctx, c.Carteira)
	if err != nil {
		t.Fatal(err)
	}
	if saldo != 1_000 {
		t.Fatalf("a recusa mexeu no saldo: %s", saldo.BRL())
	}
}

// ---------------------------------------------------------------------------
// Relatório geral do Harness
// ---------------------------------------------------------------------------

func TestRelatorioDeFitnessAprovado(t *testing.T) {
	c := novoCenario(t, 20_000)
	rel, err := c.ledger.Fitness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range rel.Checks {
		if !ch.Aprovado {
			t.Errorf("invariante violada — %s (%s): %s", ch.Nome, ch.Invariante, ch.Detalhe)
		}
	}
	if !rel.Aprovado {
		t.Fatal("o sistema esta violando pelo menos uma invariante do dominio")
	}
}
