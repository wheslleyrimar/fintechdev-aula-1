// Package money existe por um motivo só: dinheiro nunca é float.
// Toda a aplicação trabalha em centavos inteiros.
package money

import (
	"fmt"
	"strconv"
	"strings"
)

// Cents é o valor monetário em centavos. int64 aguenta ~92 quatrilhões de
// centavos — folga suficiente para o PIB, quanto mais para o ledger.
type Cents int64

func (c Cents) BRL() string {
	neg := c < 0
	v := int64(c)
	if neg {
		v = -v
	}
	reais := v / 100
	cent := v % 100

	s := strconv.FormatInt(reais, 10)
	var out []byte
	for i, ch := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, ch)
	}
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%sR$ %s,%02d", sign, string(out), cent)
}

// ParseBRL aceita "100", "100.50", "1234,56" e devolve centavos.
func ParseBRL(s string) (Cents, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "R$", ""))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, ",", ".")
	if s == "" {
		return 0, fmt.Errorf("valor vazio")
	}
	parts := strings.SplitN(s, ".", 2)
	reais, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("valor invalido: %q", s)
	}
	var cent int64
	if len(parts) == 2 {
		frac := (parts[1] + "00")[:2]
		cent, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("valor invalido: %q", s)
		}
	}
	if reais < 0 {
		return Cents(reais*100 - cent), nil
	}
	return Cents(reais*100 + cent), nil
}
