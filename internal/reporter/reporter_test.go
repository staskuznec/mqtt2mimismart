package reporter

import (
	"bytes"
	"strings"
	"testing"

	"Shelly_2.5/internal/config"
)

func TestPrepareAppliesScale(t *testing.T) {
	r := Reading{
		Point: config.Point{ID: 100, SubID: 1, Scale: 0.1},
		Value: 2300, // Вт
		Label: "мощность",
	}
	if got := prepare(r); got != 230 {
		t.Errorf("значение = %v, ожидалось 230", got)
	}
}

func TestPrepareClampsOverflow(t *testing.T) {
	// Без scale мощность в ваттах не влезает в fixed-point 8.8
	// и должна быть обрезана, а не завёрнута по модулю.
	r := Reading{Point: config.Point{ID: 100, SubID: 1}, Value: 2300, Label: "мощность"}
	got := prepare(r)
	if got != MaxSensorValue {
		t.Errorf("значение = %v, ожидалось %v", got, MaxSensorValue)
	}
}

func TestPrepareClampsNegative(t *testing.T) {
	r := Reading{Point: config.Point{ID: 100, SubID: 2}, Value: -15, Label: "ток"}
	if got := prepare(r); got != 0 {
		t.Errorf("значение = %v, ожидалось 0", got)
	}
}

func TestPrepareKeepsValidValue(t *testing.T) {
	r := Reading{Point: config.Point{ID: 100, SubID: 0}, Value: 1, Label: "реле"}
	if got := prepare(r); got != 1 {
		t.Errorf("значение = %v, ожидалось 1", got)
	}
}

func TestPrintShowsScaledValue(t *testing.T) {
	var buf bytes.Buffer
	Print(&buf, []Reading{
		{Point: config.Point{ID: 100, SubID: 0}, Value: 1, Label: "Канал 0"},
		{Point: config.Point{ID: 100, SubID: 1, Scale: 0.1}, Value: 2300, Label: "Канал 0 (Вт)"},
	})
	out := buf.String()

	for _, want := range []string{"id=100 subid=0", "Канал 0", "id=100 subid=1", "230", "исходное 2300"} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе нет %q:\n%s", want, out)
		}
	}
}

func TestPrintEmpty(t *testing.T) {
	var buf bytes.Buffer
	Print(&buf, nil)
	if !strings.Contains(buf.String(), "отправлять нечего") {
		t.Errorf("неожиданный вывод: %q", buf.String())
	}
}
