package ids

import (
	"testing"
	"time"
)

func TestE2EIDFormatoBACEN(t *testing.T) {
	id := NewE2EID("00000001", time.Date(2026, 8, 12, 10, 30, 0, 0, time.UTC))
	if len(id) != 32 {
		t.Fatalf("E2E ID deve ter 32 caracteres, veio %d: %s", len(id), id)
	}
	if err := ValidateE2EID(id); err != nil {
		t.Fatalf("E2E ID gerado deveria ser valido: %v", err)
	}
	if id[:9] != "E00000001" {
		t.Fatalf("prefixo E + ISPB esperado, veio %s", id[:9])
	}
	if id[9:21] != "202608121030" {
		t.Fatalf("timestamp AAAAMMDDHHMM esperado, veio %s", id[9:21])
	}
}

func TestE2EIDUnicidadePratica(t *testing.T) {
	vistos := map[string]bool{}
	agora := time.Now()
	for i := 0; i < 10_000; i++ {
		id := NewE2EID("00000001", agora)
		if vistos[id] {
			t.Fatalf("colisao de E2E ID na iteracao %d", i)
		}
		vistos[id] = true
	}
}

func TestValidateE2EIDRejeitaLixo(t *testing.T) {
	casos := []string{
		"",
		"E123",
		"X00000001202608121030abcdefghijk",             // nao comeca com E
		"E0000000120260812103abcdefghijkl",             // tamanho ok, data invalida
		"E00000001202608121030abcdefghijkl",            // 33 chars
	}
	for _, c := range casos {
		if err := ValidateE2EID(c); err == nil {
			t.Fatalf("deveria rejeitar %q", c)
		}
	}
}
