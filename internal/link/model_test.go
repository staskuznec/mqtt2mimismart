package link

import "testing"

// На объекте с тремя одинаковыми инверторами имя связки из профиля совпадает у
// всех: «Ток разряда АКБ» в журнале не говорит, о каком из них речь. Поэтому в
// заголовок идёт устройство.
func TestTitleNamesDevice(t *testing.T) {
	l := Link{Name: "Ток разряда АКБ", Device: "Инвертор 1"}
	if got := l.Title(); got != "Инвертор 1 · Ток разряда АКБ" {
		t.Errorf("заголовок %q", got)
	}

	// Связка вне устройства остаётся с прежним заголовком.
	if got := (Link{Name: "Свет в прихожей"}).Title(); got != "Свет в прихожей" {
		t.Errorf("заголовок %q", got)
	}

	// Без имени видно, что куда едет, — и всё равно от какого устройства.
	l = Link{Direction: In, Topic: "microart/inv2/map/_MODE", TargetID: 563, TargetSubID: 163,
		Device: "Инвертор 2"}
	if got := l.Title(); got != "Инвертор 2 · microart/inv2/map/_MODE → 563:163" {
		t.Errorf("заголовок %q", got)
	}
}
