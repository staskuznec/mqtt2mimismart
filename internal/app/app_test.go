package app

import (
	"crypto/sha256"
	"testing"
)

// Описание дома пересобирается по отпечатку, а не по длине.
//
// Правка адреса длину не меняет: «563:160» и «563:116» одинаковы по числу
// байт. Со сравнением длин элемент оставался в списке со старым адресом
// сколько угодно долго — сколько ни жми «перечитать».
func TestLogicChangeIsSeenByFingerprint(t *testing.T) {
	const before = `<house><area name="Гараж">` +
		`<item addr="563:160" length="0" name="Время до разряда" sub-type="text" type="virtual"/>` +
		`</area></house>`
	after := []byte(`<house><area name="Гараж">` +
		`<item addr="563:116" length="0" name="Время до разряда" sub-type="text" type="virtual"/>` +
		`</area></house>`)

	if len(before) != len(after) {
		t.Fatalf("описания разной длины (%d и %d) — тест ловит не то",
			len(before), len(after))
	}
	if sha256.Sum256([]byte(before)) == sha256.Sum256(after) {
		t.Error("отпечатки совпали: правка адреса осталась бы незамеченной")
	}
}
