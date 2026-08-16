package logic

import (
	"fmt"
	"sort"
	"strings"
)

// Заведение виртуальных элементов под связки.
//
// Элементы умного дома живут в logic.xml, и шлюз их только читает. Но завести
// три десятка строк руками, не сбившись в нумерации, невозможно: адреса
// придумываются по одному, а ошибка обнаруживается уже на объекте — связка
// пишет в чужой элемент. Поэтому разметку собирает шлюз: он знает, какие
// адреса на модуле уже заняты, и какая форма значения нужна каждой роли.
//
// Записывать в logic.xml шлюз не пытается: битый файл означает мёртвый объект
// целиком, а не одну неработающую связку. Он готовит текст, который человек
// вставляет через MimiSetup, — и дальше подставляет элементы по адресам сам.

// NewItem — элемент, предлагаемый к заведению.
type NewItem struct {
	ID    uint16
	SubID uint8
	Name  string

	// SubType — вид элемента в logic.xml: sensor, text или однобайтовый вроде
	// lamp. От него же зависит длина статуса.
	SubType string
	Length  int
	Dim     string // подпись единицы: V, A, °C

	// Comment — откуда элемент будет получать значение. В разметке уезжает
	// комментарием: через полгода это единственный способ понять, что здесь
	// вообще происходит.
	Comment string
}

// Addr возвращает адрес в виде "563:110".
func (i NewItem) Addr() string { return fmt.Sprintf("%d:%d", i.ID, i.SubID) }

// Формы значения, под которые заводятся элементы.
const (
	subTypeSensor = "sensor"
	subTypeText   = "text"
	subTypeLamp   = "lamp"
)

// ItemFor собирает описание элемента под форму значения связки.
//
// form — то же, что и в связке: byte, sensor или text. Однобайтовые элементы
// заводятся лампой: у неё статус ровно в один байт, и это единственный вид,
// подходящий под byte из тех, что можно создать виртуальным.
func ItemFor(id uint16, subID uint8, name, form, comment string) NewItem {
	item := NewItem{ID: id, SubID: subID, Name: name, Comment: comment}
	switch form {
	case FormSensor:
		item.SubType, item.Length = subTypeSensor, 2
	case FormByte:
		item.SubType, item.Length = subTypeLamp, 0
	default:
		item.SubType, item.Length = subTypeText, 0
	}
	return item
}

// DimFor угадывает подпись единицы по названию роли.
//
// Подпись видна в приложении рядом со значением, и приписать её вручную к трём
// десяткам элементов — та же работа, что и расставить адреса. Угадывать больше
// перечисленного не пытаемся: неверная подпись хуже отсутствующей.
func DimFor(title string) string {
	// Порядок важен: «Вт» начинается с «В», а «А·ч» — с «А», и общая проверка,
	// стоящая первой, забрала бы обе.
	switch {
	case strings.Contains(title, "°C"):
		return "°C"
	case strings.Contains(title, ", Вт"):
		return "W"
	case strings.Contains(title, ", кВт"):
		return "kWh"
	case strings.Contains(title, ", А·ч"):
		return "Ah"
	case strings.Contains(title, ", Гц"):
		return "Hz"
	case strings.Contains(title, ", В"):
		return "V"
	case strings.Contains(title, ", А"):
		return "A"
	case strings.Contains(title, ", %"):
		return "%"
	default:
		return ""
	}
}

// UsedSubIDs перечисляет занятые sub-id на модуле.
func (h House) UsedSubIDs(id uint16) map[uint8]bool {
	used := make(map[uint8]bool)
	for _, e := range h.Elements {
		if e.ID == id {
			used[e.SubID] = true
		}
	}
	return used
}

// ModuleIDs перечисляет модули, которые уже есть в logic.xml, по возрастанию.
//
// Виртуальные элементы обычно вешают на тот же модуль, где уже живут соседние:
// так адреса не расползаются по всему дому. Список подсказывает, какие есть.
func (h House) ModuleIDs() []uint16 {
	seen := make(map[uint16]bool)
	out := make([]uint16, 0, 8)
	for _, e := range h.Elements {
		if !seen[e.ID] {
			seen[e.ID] = true
			out = append(out, e.ID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// FreeSubIDs выдаёт n свободных sub-id на модуле, начиная с from.
//
// Занятые пропускаются: перезаписать чужой адрес значит увести показания в
// работающий элемент, и заметят это далеко не сразу.
func (h House) FreeSubIDs(id uint16, n int, from uint8) ([]uint8, error) {
	if n <= 0 {
		return nil, nil
	}

	used := h.UsedSubIDs(id)
	out := make([]uint8, 0, n)
	for sub := int(from); sub <= 255 && len(out) < n; sub++ {
		if !used[uint8(sub)] {
			out = append(out, uint8(sub))
		}
	}
	if len(out) < n {
		return nil, fmt.Errorf("на модуле %d свободных адресов %d, нужно %d: "+
			"возьмите другой модуль или начните с меньшего sub-id", id, len(out), n)
	}
	return out, nil
}

// NextFreeSubID подсказывает, с какого адреса начинать.
//
// Не первый свободный, а следующий за последним занятым: дырки в нумерации
// обычно оставлены нарочно — под то, что дописывают в уже заведённую область.
func (h House) NextFreeSubID(id uint16) uint8 {
	last := -1
	for _, e := range h.Elements {
		if e.ID == id && int(e.SubID) > last {
			last = int(e.SubID)
		}
	}
	if last < 0 || last >= 255 {
		return 0
	}
	return uint8(last + 1)
}

// RenderArea собирает область logic.xml из подготовленных элементов.
//
// Отступы и порядок атрибутов — как в файлах MimiSetup: разметку вставляют в
// существующий logic.xml, и чужеродный кусок в нём читается хуже.
func RenderArea(area string, items []NewItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "        <area name=%q>\n", area)
	for _, it := range items {
		b.WriteString("            <item")
		fmt.Fprintf(&b, " addr=%q", it.Addr())
		if it.Dim != "" {
			fmt.Fprintf(&b, " dim=%q", it.Dim)
		}
		fmt.Fprintf(&b, " length=%q", fmt.Sprint(it.Length))
		fmt.Fprintf(&b, " name=%q", it.Name)
		fmt.Fprintf(&b, " sub-type=%q", it.SubType)
		b.WriteString(" type=\"virtual\"/>")
		if it.Comment != "" {
			fmt.Fprintf(&b, " <!-- %s -->", it.Comment)
		}
		b.WriteString("\n")
	}
	b.WriteString("        </area>\n")
	return b.String()
}
