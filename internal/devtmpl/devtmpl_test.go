package devtmpl

import (
	"strconv"
	"strings"
	"testing"

	"github.com/staskuznec/mqtt2mimismart/internal/link"
)

// Встроенные шаблоны обязаны быть валидными: их применяют один раз и потом
// доверяют им, а неверная роль означает связку без элемента.
func TestBuiltinTemplatesValid(t *testing.T) {
	all, err := Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("встроенных шаблонов нет")
	}
	for _, tmpl := range all {
		t.Run(tmpl.Key, func(t *testing.T) {
			if err := tmpl.Validate(); err != nil {
				t.Errorf("шаблон невалиден: %v", err)
			}
			if tmpl.Model == "" {
				t.Error("не указана модель — автоподбор по announce не сработает")
			}
		})
	}
}

// Полное применение: назначены все роли.
func TestApplyFull(t *testing.T) {
	tmpl, err := Find("shelly25-relay")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	assign := map[string]Addr{}
	for i, r := range tmpl.Roles {
		assign[r.Key] = Addr{ID: uint16(700 + i), SubID: uint8(i)}
	}

	links, err := tmpl.Apply("shellies/shellyswitch25-4022D8956527", assign)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(links) != len(tmpl.Links) {
		t.Errorf("развернулось %d связок, ожидалось %d", len(links), len(tmpl.Links))
	}
	for _, l := range links {
		if strings.Contains(l.Topic, Placeholder) {
			t.Errorf("в топике осталась подстановка: %q", l.Topic)
		}
		if err := l.Validate(); err != nil {
			t.Errorf("связка %q невалидна: %v", l.Name, err)
		}
	}
}

// Роль без назначенного элемента пропускается: не нужны показания — не
// назначайте их, и связок под них не появится.
func TestApplySkipsUnassignedRoles(t *testing.T) {
	tmpl, _ := Find("shelly25-relay")

	links, err := tmpl.Apply("shellies/sw25-A1", map[string]Addr{
		"ch0": {ID: 758, SubID: 4},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Только пара по каналу 0.
	if len(links) != 2 {
		t.Fatalf("развернулось %d связок, ожидалось 2: %+v", len(links), links)
	}
	for _, l := range links {
		if l.TargetID != 758 || l.TargetSubID != 4 {
			t.Errorf("связка ушла не на тот элемент: %s", l.Addr())
		}
	}
}

// Обязательная роль без элемента — отказ: устройство без управления это не
// устройство, а набор показаний.
func TestApplyRequiresRequiredRoles(t *testing.T) {
	tmpl, _ := Find("shelly25-relay")

	if _, err := tmpl.Apply("shellies/sw25-A1", map[string]Addr{"temp": {ID: 542, SubID: 16}}); err == nil {
		t.Error("шаблон применён без обязательной роли")
	}
}

func TestApplyRequiresPrefix(t *testing.T) {
	tmpl, _ := Find("shelly25-relay")
	if _, err := tmpl.Apply("  ", map[string]Addr{"ch0": {ID: 1, SubID: 1}}); err == nil {
		t.Error("пустой префикс принят")
	}
}

// Лишний слэш в префиксе — самая частая опечатка при копировании из снифера.
func TestApplyTrimsPrefix(t *testing.T) {
	tmpl, _ := Find("shelly25-relay")

	links, err := tmpl.Apply("shellies/sw25-A1/", map[string]Addr{"ch0": {ID: 758, SubID: 4}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if links[0].Topic != "shellies/sw25-A1/relay/0" {
		t.Errorf("топик = %q", links[0].Topic)
	}
}

// Канал реле разворачивается парой: состояние и команда на один элемент.
func TestApplyPairsChannel(t *testing.T) {
	tmpl, _ := Find("shelly25-relay")

	links, _ := tmpl.Apply("shellies/sw25-A1", map[string]Addr{"ch0": {ID: 758, SubID: 4}})

	var in, out *link.Link
	for i := range links {
		switch links[i].Direction {
		case link.In:
			in = &links[i]
		case link.Out:
			out = &links[i]
		}
	}
	if in == nil || out == nil {
		t.Fatal("канал развернулся не парой")
	}
	if out.Topic != in.Topic+"/command" {
		t.Errorf("командный топик = %q, ожидался %q", out.Topic, in.Topic+"/command")
	}
	if out.Retain {
		t.Error("команда с retain — устройство сработает само при включении")
	}
}

// Энергия у Shelly считается в ватт-минутах: ошибка в множителе тихая и
// ровно в шестьдесят раз.
func TestEnergyScale(t *testing.T) {
	tmpl, _ := Find("shelly25-relay")

	links, _ := tmpl.Apply("shellies/sw25-A1", map[string]Addr{
		"ch0": {ID: 758, SubID: 4}, "energy0": {ID: 563, SubID: 121},
	})

	for _, l := range links {
		if l.TargetSubID != 121 {
			continue
		}
		w, err := l.ToWire([]byte("211200")) // 211200 Вт·мин = 3.52 кВт·ч
		if err != nil {
			t.Fatalf("ToWire: %v", err)
		}
		if w.Text != "3.5 кВт" {
			t.Errorf("энергия = %q, ожидалось %q", w.Text, "3.5 кВт")
		}
		return
	}
	t.Fatal("связка энергии не найдена")
}

func TestParseRejectsBroken(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"не json", `{`},
		{"без названия", `{"links":[{"role":"a","topic":"{{prefix}}/x","direction":"in"}],"roles":[{"key":"a"}]}`},
		{"без связок", `{"name":"Тест","roles":[]}`},
		{"роль не описана", `{"name":"Т","roles":[{"key":"a"}],"links":[{"role":"нет","topic":"{{prefix}}/x","direction":"in"}]}`},
		{"топик без подстановки", `{"name":"Т","roles":[{"key":"a"}],"links":[{"role":"a","topic":"x","direction":"in"}]}`},
		{"неверное направление", `{"name":"Т","roles":[{"key":"a"}],"links":[{"role":"a","topic":"{{prefix}}/x","direction":"вбок"}]}`},
		{"роль дважды", `{"name":"Т","roles":[{"key":"a"},{"key":"a"}],"links":[{"role":"a","topic":"{{prefix}}/x","direction":"in"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.body)); err == nil {
				t.Error("битый шаблон принят")
			}
		})
	}
}

// Единицы во всех профилях приведены к одному виду: мощность и напряжение без
// знаков после запятой, энергия — с одним, в киловаттах. Разнобой замечают уже
// на объекте, где показания стоят рядом на одном экране.
func TestUnitsAreConsistent(t *testing.T) {
	all, err := Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}

	want := map[string]int{
		" Вт":  0,
		" В":   0,
		" кВт": 1,
		// Ток бытовых нагрузок измеряется сотыми долями ампера: без двух
		// знаков лампочка на семь ватт показывала бы ровный ноль.
		" А": 2,
		" %": 0,
		// Освещённость идёт текстом, а не показанием датчика: тысячи люксов
		// не помещаются в fixed-point 8.8 и обрезались бы до 255.
		" лк": 0,
	}

	for _, tmpl := range all {
		for _, l := range tmpl.Links {
			if l.Unit == "" {
				continue
			}
			digits, known := want[l.Unit]
			if !known {
				t.Errorf("%s, связка «%s»: единица %q не из общего списка",
					tmpl.Key, l.Name, l.Unit)
				continue
			}
			if l.Precision == nil || *l.Precision != digits {
				got := "не задано"
				if l.Precision != nil {
					got = strconv.Itoa(*l.Precision)
				}
				t.Errorf("%s, связка «%s»: у %q знаков после запятой %s, ожидалось %d",
					tmpl.Key, l.Name, l.Unit, got, digits)
			}
		}
	}
}
