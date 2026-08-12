package reporter

import (
	"testing"

	"github.com/staskuznec/mqtt2mimismart/internal/config"
)

// Единица в однобайтовом элементе должна уехать как байт 1, а не как
// младший байт fixed-point 8.8, который равен нулю.
func TestSingleByteState(t *testing.T) {
	on := Reading{Point: config.Point{ID: 563, SubID: 250, As: config.AsByte}, Value: 1}
	off := Reading{Point: config.Point{ID: 563, SubID: 250, As: config.AsByte}, Value: 0}

	if got := SingleByte(on); got != 1 {
		t.Errorf("включено = %d, ожидалась 1", got)
	}
	if got := SingleByte(off); got != 0 {
		t.Errorf("выключено = %d, ожидался 0", got)
	}
	if !on.Point.AsSingleByte() {
		t.Error("точка не распознана как однобайтовая")
	}

	// Для сравнения: как это выглядело бы датчиком.
	sensor := Reading{Point: config.Point{ID: 563, SubID: 250}, Value: 1}
	if raw := uint16(prepare(sensor) * 256); byte(raw) != 0 {
		t.Errorf("младший байт датчика = %d, а был бы 0 — тогда лампа не включалась", byte(raw))
	}
}
