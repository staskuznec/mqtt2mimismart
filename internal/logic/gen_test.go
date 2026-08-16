package logic

import (
	"encoding/xml"
	"strings"
	"testing"
)

func house() House {
	return House{Elements: []Element{
		{ID: 563, SubID: 110, Name: "В сети", Type: "virtual"},
		{ID: 563, SubID: 111, Name: "Напряжение", Type: "virtual"},
		{ID: 563, SubID: 114, Name: "Заряд", Type: "virtual"},
		{ID: 758, SubID: 4, Name: "Свет", Type: "lamp"},
	}}
}

// Занятый адрес пропускается. Выдать его повторно значит увести показания в
// работающий элемент — ошибка тихая и обнаруживается уже на объекте.
func TestFreeSubIDsSkipsTaken(t *testing.T) {
	got, err := house().FreeSubIDs(563, 4, 110)
	if err != nil {
		t.Fatalf("FreeSubIDs: %v", err)
	}
	want := []uint8{112, 113, 115, 116}
	if len(got) != len(want) {
		t.Fatalf("выдано %v, ожидалось %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("выдано %v, ожидалось %v", got, want)
		}
	}
}

// Модуль кончается на 255, и молча выдать меньше адресов, чем просили, нельзя:
// половина элементов завелась бы, половина нет.
func TestFreeSubIDsRefusesWhenNoRoom(t *testing.T) {
	if _, err := house().FreeSubIDs(563, 10, 250); err == nil {
		t.Error("выдано больше адресов, чем осталось на модуле")
	}
}

// Подсказываем первый свободный адрес: занятые дальше всё равно пропускаются,
// а отсчёт от последнего занятого упирался в конец модуля на живом объекте.
func TestNextFreeSubID(t *testing.T) {
	if got := house().NextFreeSubID(563); got != 0 {
		t.Errorf("подсказан адрес %d, ожидался 0", got)
	}
	if got := house().NextFreeSubID(999); got != 0 {
		t.Errorf("на пустом модуле подсказан адрес %d, ожидался 0", got)
	}
}

func TestModuleIDs(t *testing.T) {
	got := house().ModuleIDs()
	if len(got) != 2 || got[0] != 563 || got[1] != 758 {
		t.Errorf("модули %v, ожидались [563 758]", got)
	}
}

// Форма значения решает вид элемента: показание занимает два байта, всё
// остальное — динамическую длину. Однобайтовое состояние заводится лампой:
// других виртуальных однобайтовых видов просто нет.
func TestItemForByForm(t *testing.T) {
	for _, tc := range []struct {
		form    string
		subType string
		length  int
	}{
		{FormSensor, "sensor", 2},
		{FormByte, "lamp", 0},
		{FormText, "text", 0},
		{"", "text", 0},
	} {
		it := ItemFor(563, 120, "Проба", tc.form, "")
		if it.SubType != tc.subType || it.Length != tc.length {
			t.Errorf("форма %q дала %s/%d, ожидалось %s/%d",
				tc.form, it.SubType, it.Length, tc.subType, tc.length)
		}
	}
}

func TestDimFor(t *testing.T) {
	for title, want := range map[string]string{
		"Температура АКБ, °C":     "°C",
		"Напряжение сети, В":      "V",
		"Ток АКБ, А":              "A",
		"Мощность нагрузки, Вт":   "W",
		"Частота сети, Гц":        "Hz",
		"Заряд АКБ, %":            "%",
		"Остаточная ёмкость, А·ч": "Ah",
		"Выработка за сутки, кВт": "kWh",
		"Режим МАП, код":          "",
	} {
		if got := DimFor(title); got != want {
			t.Errorf("%q → %q, ожидалось %q", title, got, want)
		}
	}
}

// Разметку вставляют в живой logic.xml, поэтому она обязана разбираться. Кусок
// с поехавшими кавычками убил бы весь объект, а не одну связку.
func TestRenderAreaIsValidXML(t *testing.T) {
	items := []NewItem{
		ItemFor(563, 115, "Ток заряда", FormSensor, "bat/0/Iavg"),
		ItemFor(563, 116, "Режим МАП", FormText, "map/_MODE"),
	}
	items[0].Dim = "A"

	out := RenderArea("Инвертор1", items)
	if err := xml.Unmarshal([]byte(out), new(struct {
		XMLName xml.Name `xml:"area"`
	})); err != nil {
		t.Fatalf("разметка не разбирается: %v\n%s", err, out)
	}

	for _, want := range []string{
		`addr="563:115" dim="A" length="2" name="Ток заряда" sub-type="sensor" type="virtual"`,
		`addr="563:116" length="0" name="Режим МАП" sub-type="text" type="virtual"`,
		`<!-- bat/0/Iavg -->`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в разметке нет строки:\n%s\nполучилось:\n%s", want, out)
		}
	}
}

// Отсчёт от первого свободного, а не от последнего занятого.
//
// На объекте у модуля были элементы вплоть до 250-го адреса, и заведение
// падало с «свободно 5, нужно 10», хотя между областями пустовало полсотни
// адресов. Разрывы в нумерации — обычное дело, и заполнять их безопасно:
// адрес с областью никак не связан.
func TestNextFreeSubIDFillsGaps(t *testing.T) {
	h := House{Elements: []Element{
		{ID: 563, SubID: 0}, {ID: 563, SubID: 1},
		{ID: 563, SubID: 100}, {ID: 563, SubID: 250},
	}}

	if got := h.NextFreeSubID(563); got != 2 {
		t.Errorf("подсказан адрес %d, ожидался 2 — первый свободный", got)
	}

	// Десять адресов начиная со второго находятся сразу.
	subs, err := h.FreeSubIDs(563, 10, 2)
	if err != nil {
		t.Fatalf("FreeSubIDs: %v", err)
	}
	if len(subs) != 10 || subs[0] != 2 {
		t.Errorf("выдано %v", subs)
	}
}

// Когда с указанного места мест не хватает, отказ обязан сказать, сколько
// свободно на модуле целиком и с какого адреса начинать: иначе «свободно 5,
// нужно 10» выглядит как «модуль кончился», хотя это не так.
func TestFreeSubIDsSuggestsWhereToStart(t *testing.T) {
	elements := make([]Element, 0, 60)
	for sub := 100; sub < 160; sub++ {
		elements = append(elements, Element{ID: 563, SubID: uint8(sub)})
	}
	h := House{Elements: elements}

	_, err := h.FreeSubIDs(563, 10, 250)
	if err == nil {
		t.Fatal("отказа не было")
	}
	for _, want := range []string{"всего на модуле свободно", "начните с 0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в отказе нет «%s»: %v", want, err)
		}
	}
}
