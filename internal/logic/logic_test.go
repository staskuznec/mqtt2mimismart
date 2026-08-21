package logic

import "testing"

// Кусок настоящего logic.xml: вложенные области, элементы разных типов,
// имя с разделителем строк и одна битая строка.
const sample = `<?xml version="1.0" encoding="UTF-8"?>
<smart-house srv-ver="2.14">
  <item addr="98:1" name="Служебный" type="script"/>
  <area name="Первый этаж">
    <area name="Прихожая" name-ru="Прихожая">
      <item addr="773:1" cfgid="17" name="Свет" type="lamp"/>
      <item addr="542:16" cfgid="40" name="д.т. Прихожая" type="temperature-sensor"/>
      <item addr="585:249" name="Климат\10прихожая" type="conditioner"/>
      <item addr="битый" name="Не разобрать" type="lamp"/>
    </area>
    <area name="Кухня">
      <item addr="773:2" name="Свет" type="lamp"/>
    </area>
  </area>
</smart-house>`

func TestParse(t *testing.T) {
	house, err := Parse([]byte(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if house.Version != "2.14" {
		t.Errorf("версия = %q, ожидалась 2.14", house.Version)
	}
	// Пять корректных элементов; битый адрес пропущен.
	if len(house.Elements) != 5 {
		t.Fatalf("элементов %d, ожидалось 5: %+v", len(house.Elements), house.Elements)
	}
}

// Одна странная строка не повод остаться без списка элементов вовсе:
// описание приходит от чужого сервера.
func TestParseSkipsBadAddr(t *testing.T) {
	house, _ := Parse([]byte(sample))
	for _, e := range house.Elements {
		if e.Name == "Не разобрать" {
			t.Error("элемент с неразбираемым адресом попал в список")
		}
	}
}

func TestParseNestedAreas(t *testing.T) {
	house, _ := Parse([]byte(sample))

	e, ok := house.Find(773, 1)
	if !ok {
		t.Fatal("элемент 773:1 не найден")
	}
	if e.Area != "Первый этаж → Прихожая" {
		t.Errorf("область = %q, ожидался полный путь", e.Area)
	}
	if e.Name != "Свет" || e.Type != "lamp" {
		t.Errorf("элемент = %+v", e)
	}
}

// Элементы вне областей тоже должны попадать в список.
func TestParseTopLevelItems(t *testing.T) {
	house, _ := Parse([]byte(sample))

	e, ok := house.Find(98, 1)
	if !ok {
		t.Fatal("элемент верхнего уровня не найден")
	}
	if e.Area != "" {
		t.Errorf("область = %q, ожидалась пустая", e.Area)
	}
}

// В именах встречается "\10" как разделитель строк интерфейса умного дома.
// В списке выбора он выглядит мусором.
func TestParseCleansName(t *testing.T) {
	house, _ := Parse([]byte(sample))

	e, _ := house.Find(585, 249)
	if e.Name != "Климат прихожая" {
		t.Errorf("имя = %q, ожидалось %q", e.Name, "Климат прихожая")
	}
}

// Подсказка формы значения — главное, ради чего типы вообще читаются:
// лампе нужен байт, датчику — 8.8, и перепутать их стоит дорого.
func TestForm(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		want string
	}{
		{"lamp", FormByte},
		{"script", FormByte},
		{"curtain", FormByte},
		{"door-sensor", FormByte},
		{"temperature-sensor", FormSensor},
		{"humidity-sensor", FormSensor},
		{"conditioner", FormAny},
		{"неизвестный", FormAny},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			if got := (Element{Type: tc.typ}).Form(); got != tc.want {
				t.Errorf("Form() = %q, ожидалось %q", got, tc.want)
			}
		})
	}
}

func TestAreas(t *testing.T) {
	house, _ := Parse([]byte(sample))
	areas := house.Areas()

	// Пустая область (элементы верхнего уровня) тоже считается.
	if len(areas) != 3 {
		t.Errorf("областей %d, ожидалось 3: %v", len(areas), areas)
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Error("пустое описание принято без ошибки")
	}
	if _, err := Parse([]byte("не xml")); err == nil {
		t.Error("мусор принят как описание дома")
	}
}

func TestAddr(t *testing.T) {
	if got := (Element{ID: 563, SubID: 57}).Addr(); got != "563:57" {
		t.Errorf("Addr() = %q", got)
	}
}

// Элементы под шлюз заводят виртуальными, и в logic.xml они все одного типа —
// "virtual". Чем такой элемент работает на деле, сказано в sub-type: без него
// датчик протечки и строка выглядят одинаково, и подсказать форму значения
// нечем — а ошибка здесь стоит дороже всего.
func TestVirtualElementTakesKindFromSubType(t *testing.T) {
	house, err := Parse([]byte(`<smart-house srv-ver="1">
		<area name="Душ">
			<item addr="50:12" length="0" name="душ Протечка" sub-type="leak-sensor" type="virtual"/>
			<item addr="50:13" length="2" name="душ Температура" sub-type="sensor" type="virtual"/>
			<item addr="50:14" length="0" name="душ Режим" sub-type="text" type="virtual"/>
			<item addr="50:15" length="0" name="душ Свет" type="lamp"/>
		</area>
	</smart-house>`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for _, tc := range []struct {
		addr string
		kind string
		form string
	}{
		{"50:12", "leak-sensor", FormByte},
		{"50:13", "sensor", FormSensor},
		{"50:14", "text", FormText},
		{"50:15", "lamp", FormByte},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			var found bool
			for _, e := range house.Elements {
				if e.Addr() != tc.addr {
					continue
				}
				found = true
				if got := e.Kind(); got != tc.kind {
					t.Errorf("Kind = %q, ожидалось %q", got, tc.kind)
				}
				if got := e.Form(); got != tc.form {
					t.Errorf("Form = %q, ожидалось %q", got, tc.form)
				}
			}
			if !found {
				t.Fatalf("элемент %s не разобран", tc.addr)
			}
		})
	}
}
