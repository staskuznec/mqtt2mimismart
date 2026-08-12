// Package logic разбирает logic.xml — описание умного дома, которое сервер
// присылает в ответе на рукопожатие.
//
// Нужен он ради одного: чтобы в редакторе связки элемент выбирался из списка с
// настоящими именами и областями — «Прихожая → Свет (lamp, 773:1)», — а не
// набирался руками как «773:1». Набранный руками адрес ошибается молча:
// значение уходит не тому элементу, и найти это потом трудно.
//
// Разбирается только то, что нужно для выбора: области, адреса, имена и типы.
// Остальные полторы сотни атрибутов — про интерфейс умного дома, и нам они
// безразличны.
package logic

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Element — элемент умного дома.
type Element struct {
	ID    uint16
	SubID uint8
	Name  string
	Type  string // lamp, script, temperature-sensor, dimer-lamp и так далее
	Area  string // путь области: «Первый этаж → Прихожая»
}

// Addr возвращает адрес в виде "id:subid".
func (e Element) Addr() string { return fmt.Sprintf("%d:%d", e.ID, e.SubID) }

// Форма значения, подходящая типу элемента.
const (
	FormByte   = "byte"   // состояние занимает ровно один байт
	FormSensor = "sensor" // показание в формате fixed-point 8.8
	FormText   = "text"   // строка
	FormAny    = ""       // тип неизвестен, подсказать нечего
)

// byteTypes — типы, у которых состояние занимает ровно один байт.
//
// Это главная подсказка редактора и самая дорогая ошибка в проекте: если
// такому элементу отправить значение датчика, единица уедет как 00 01,
// младший байт окажется нулевым, и элемент останется выключенным. Выключение
// при этом «работает», потому что ноль даёт нули в обоих байтах.
var byteTypes = map[string]bool{
	"lamp":          true,
	"dimer-lamp":    true,
	"script":        true,
	"valve":         true,
	"curtain":       true,
	"gate":          true,
	"door-sensor":   true,
	"leak-sensor":   true,
	"switch":        true,
	"motion-sensor": true,
}

// sensorTypes — типы, чьё значение передаётся как показание датчика.
var sensorTypes = map[string]bool{
	"temperature-sensor": true,
	"humidity-sensor":    true,
	"lux-sensor":         true,
	"co2-sensor":         true,
}

// Form подсказывает форму значения по типу элемента.
func (e Element) Form() string {
	switch {
	case byteTypes[e.Type]:
		return FormByte
	case sensorTypes[e.Type]:
		return FormSensor
	default:
		return FormAny
	}
}

// House — разобранное описание дома.
type House struct {
	Version  string
	Elements []Element
}

// Find ищет элемент по адресу.
func (h House) Find(id uint16, subID uint8) (Element, bool) {
	for _, e := range h.Elements {
		if e.ID == id && e.SubID == subID {
			return e, true
		}
	}
	return Element{}, false
}

// Areas перечисляет области в порядке появления.
func (h House) Areas() []string {
	seen := make(map[string]struct{}, len(h.Elements))
	var out []string
	for _, e := range h.Elements {
		if _, ok := seen[e.Area]; ok {
			continue
		}
		seen[e.Area] = struct{}{}
		out = append(out, e.Area)
	}
	sort.Strings(out)
	return out
}

// Разбор XML. Структуры описывают только нужные поля: остальное пропускается.
type xmlHouse struct {
	XMLName xml.Name  `xml:"smart-house"`
	SrvVer  string    `xml:"srv-ver,attr"`
	Areas   []xmlArea `xml:"area"`
	Items   []xmlItem `xml:"item"`
}

type xmlArea struct {
	Name   string    `xml:"name,attr"`
	NameRu string    `xml:"name-ru,attr"`
	Areas  []xmlArea `xml:"area"`
	Items  []xmlItem `xml:"item"`
}

type xmlItem struct {
	Addr string `xml:"addr,attr"`
	Name string `xml:"name,attr"`
	Type string `xml:"type,attr"`
}

// Parse разбирает logic.xml.
//
// Элементы с неразбираемым адресом пропускаются, а не роняют разбор: описание
// приходит от чужого сервера, и одна странная строка не повод остаться без
// списка элементов вовсе.
func Parse(data []byte) (House, error) {
	if len(data) == 0 {
		return House{}, fmt.Errorf("logic: описание дома пустое")
	}

	var doc xmlHouse
	if err := xml.Unmarshal(data, &doc); err != nil {
		return House{}, fmt.Errorf("logic: разбор logic.xml: %w", err)
	}

	house := House{Version: doc.SrvVer}
	// Элементы верхнего уровня — вне областей.
	house.Elements = append(house.Elements, collectItems(doc.Items, "")...)
	for _, area := range doc.Areas {
		house.Elements = append(house.Elements, collectArea(area, "")...)
	}

	sort.Slice(house.Elements, func(i, j int) bool {
		a, b := house.Elements[i], house.Elements[j]
		if a.Area != b.Area {
			return a.Area < b.Area
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.SubID < b.SubID
	})
	return house, nil
}

// collectArea обходит область вместе с вложенными.
func collectArea(area xmlArea, parent string) []Element {
	name := area.NameRu
	if name == "" {
		name = area.Name
	}
	path := name
	if parent != "" {
		path = parent + " → " + name
	}

	out := collectItems(area.Items, path)
	for _, nested := range area.Areas {
		out = append(out, collectArea(nested, path)...)
	}
	return out
}

func collectItems(items []xmlItem, area string) []Element {
	out := make([]Element, 0, len(items))
	for _, item := range items {
		id, subID, err := parseAddr(item.Addr)
		if err != nil {
			continue
		}
		out = append(out, Element{
			ID:    id,
			SubID: subID,
			// В именах встречается "\10" как разделитель строк в интерфейсе
			// умного дома. В списке выбора он выглядит мусором.
			Name: strings.ReplaceAll(item.Name, `\10`, " "),
			Type: item.Type,
			Area: area,
		})
	}
	return out
}

// parseAddr разбирает адрес вида "563:57".
func parseAddr(s string) (uint16, uint8, error) {
	idStr, subStr, found := strings.Cut(strings.TrimSpace(s), ":")
	if !found {
		return 0, 0, fmt.Errorf("адрес %q: ожидался формат id:subid", s)
	}
	id, err := strconv.ParseUint(idStr, 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("адрес %q: id должен быть числом 0..65535", s)
	}
	sub, err := strconv.ParseUint(subStr, 10, 8)
	if err != nil {
		return 0, 0, fmt.Errorf("адрес %q: subid должен быть числом 0..255", s)
	}
	return uint16(id), uint8(sub), nil
}
