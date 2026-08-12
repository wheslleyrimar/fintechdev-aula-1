// Package ids gera e valida identificadores — em especial o EndToEndId do Pix.
//
// Aula 1 · §4.4: o BACEN já projetou a idempotência do Pix. O E2E ID tem
// 32 caracteres e acompanha a transação do pagador ao recebedor, atravessando
// o SPI inteiro. A regra "E2E ID é único" é o que torna o efeito exactly-once
// uma obrigação regulatória, e não uma boa prática opcional.
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

const alfanumerico = "0123456789abcdefghijklmnopqrstuvwxyz"

// E2E ID = "E" + ISPB(8) + AAAAMMDDHHMM(12) + aleatório(11) = 32 caracteres.
var e2eRe = regexp.MustCompile(`^E[0-9]{8}[0-9]{12}[A-Za-z0-9]{11}$`)

// NewE2EID monta um E2E ID. Em produção quem o gera é o PSP do PAGADOR, e —
// detalhe que decide tudo — a chave nasce no cliente e SOBREVIVE aos retries.
// Se o servidor gerasse a chave, cada retry ganharia chave nova e a
// deduplicação simplesmente não funcionaria (§4.3, propriedade 1).
func NewE2EID(ispb string, t time.Time) string {
	if len(ispb) != 8 {
		ispb = fmt.Sprintf("%08s", ispb)
	}
	return "E" + ispb + t.UTC().Format("200601021504") + randString(11)
}

func ValidateE2EID(s string) error {
	if len(s) != 32 {
		return fmt.Errorf("EndToEndId deve ter 32 caracteres, veio com %d", len(s))
	}
	if !e2eRe.MatchString(s) {
		return fmt.Errorf("EndToEndId fora do padrao BACEN (E + ISPB + AAAAMMDDHHMM + 11 alfanumericos)")
	}
	return nil
}

// NewUUID devolve um UUID v4. Sem dependência externa: 16 bytes aleatórios
// com os bits de versão/variante no lugar certo.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sem fonte de entropia: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func randString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("sem fonte de entropia: " + err.Error())
	}
	for i := range b {
		b[i] = alfanumerico[int(b[i])%len(alfanumerico)]
	}
	return string(b)
}
