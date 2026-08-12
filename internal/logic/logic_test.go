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
