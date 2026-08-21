package web

import (
	"testing"

	"github.com/staskuznec/mqtt2mimismart/internal/devtmpl"
)

// Элемент под датчик протечки заводят виртуальным, а вид пишут в sub-type:
// <item addr="50:12" name="душ Протечка" sub-type="lamp" type="virtual"/>.
// Роль обязана такой элемент увидеть — иначе в форме устройства выбирать
// нечего, и профиль применить не получается вовсе.
//
// Лампа тут не для красоты: виртуальным элементам документация разрешает
// sub-type="lamp", а leak-sensor не разрешает вовсе — и настоящему leak-sensor
// запись статуса означает время игнорирования протечки, а не протечку.
func TestRoleTakesVirtualElementBySubType(t *testing.T) {
	tmpl, err := devtmpl.Find("shellyflood")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	all := []elementOption{
		{Addr: "50:12", Label: "Душ → Протечка", Type: "virtual", Kind: "lamp"},
		{Addr: "50:13", Label: "Душ → Температура", Type: "virtual", Kind: "sensor"},
		{Addr: "50:14", Label: "Двор → Полив", Type: "valve", Kind: "valve"},
	}

	var flood roleOption
	for _, opt := range (&server{}).roleOptions(tmpl, all, nil) {
		if opt.Role.Key == "flood" {
			flood = opt
		}
	}
	if flood.Role.Key == "" {
		t.Fatal("в профиле датчика протечки нет роли flood")
	}

	var seen []string
	for _, e := range flood.Elements {
		seen = append(seen, e.Addr)
	}
	if !contains(seen, "50:12") {
		t.Errorf("виртуальный датчик протечки не попал в список роли: %v", seen)
	}
	// Клапан под роль не годится, и если он в списке — значит сужение не
	// сработало вовсе и проверка выше ничего не доказывает.
	if contains(seen, "50:14") {
		t.Errorf("под роль протечки предложен клапан: %v", seen)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
