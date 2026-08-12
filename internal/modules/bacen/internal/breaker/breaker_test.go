package breaker

import (
	"errors"
	"testing"
	"time"
)

func TestAbreDepoisDoLimiteEFalhaRapido(t *testing.T) {
	b := New(3, 50*time.Millisecond)
	falha := errors.New("dict fora do ar")

	for i := 0; i < 3; i++ {
		if err := b.Executar(func() error { return falha }); !errors.Is(err, falha) {
			t.Fatalf("erro da dependencia deveria vazar, veio %v", err)
		}
	}
	if b.Estado() != Aberto {
		t.Fatalf("circuito deveria estar aberto, esta %s", b.Estado())
	}

	chamou := false
	err := b.Executar(func() error { chamou = true; return nil })
	if !errors.Is(err, ErrCircuitoAberto) {
		t.Fatalf("esperava ErrCircuitoAberto, veio %v", err)
	}
	if chamou {
		t.Fatal("com circuito aberto a dependencia externa NAO pode ser chamada")
	}
}

func TestMeioAbertoFechaComSucesso(t *testing.T) {
	b := New(1, 20*time.Millisecond)
	_ = b.Executar(func() error { return errors.New("timeout") })
	if b.Estado() != Aberto {
		t.Fatal("deveria abrir")
	}

	time.Sleep(30 * time.Millisecond)
	if err := b.Executar(func() error { return nil }); err != nil {
		t.Fatalf("meio-aberto deveria deixar passar: %v", err)
	}
	if b.Estado() != Fechado {
		t.Fatalf("sucesso no meio-aberto deveria fechar, esta %s", b.Estado())
	}
}

func TestMeioAbertoReabreComFalha(t *testing.T) {
	b := New(1, 20*time.Millisecond)
	_ = b.Executar(func() error { return errors.New("timeout") })
	time.Sleep(30 * time.Millisecond)
	_ = b.Executar(func() error { return errors.New("timeout de novo") })
	if b.Estado() != Aberto {
		t.Fatalf("falha no meio-aberto deveria reabrir, esta %s", b.Estado())
	}
}
