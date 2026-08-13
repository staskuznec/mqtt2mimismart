// Package devtmpl — шаблоны устройств.
//
// Шаблон описывает, какие связки нужны устройству определённой модели, и какие
// элементы умного дома для них требуется назначить. Применение шаблона
// разворачивает десяток связок за одно действие: остаётся выбрать элементы, а
// топики, формы значений, единицы и множители уже расставлены.
//
// Главное свойство — шаблон это данные, а не код. Новая модель добавляется
// файлом, без пересборки и без релиза. Ровно ради этого движок и делался
// настраиваемым.
package devtmpl

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/staskuznec/mqtt2mimismart/internal/link"
	"github.com/staskuznec/mqtt2mimismart/profiles"
)

// templateFS — профили, которые едут вместе со шлюзом. Сами файлы лежат в
// каталоге profiles в корне проекта: это данные, и прятать их в глубине
// internal незачем.
var templateFS = profiles.FS

// Placeholder — подстановка префикса топиков устройства.
const Placeholder = "{{prefix}}"

// Template — шаблон устройства.
type Template struct {
	// Key — короткое имя файла без расширения, оно же идентификатор.
	Key string `json:"-"`

	Name  string `json:"name"`
	Model string `json:"model"` // как устройство представляется в announce
	Note  string `json:"note"`  // что важно знать перед применением

	Roles []Role     `json:"roles"`
	Links []LinkSpec `json:"links"`
}

// Role — то, что при применении шаблона нужно назначить: элемент умного дома
// под конкретное назначение устройства.
type Role struct {
	Key      string `json:"key"`      // "lamp0"
	Title    string `json:"title"`    // "Канал 0 — свет"
	Form     string `json:"form"`     // какая форма значения ожидается
	Hint     string `json:"hint"`     // подсказка, какой элемент подходит
	Required bool   `json:"required"` // без него шаблон не применить

	// Types — типы элементов умного дома, которые сюда годятся: "lamp",
	// "temperature-sensor" и прочие из logic.xml. Список сужает выбор в форме
	// до подходящих, чтобы не искать лампу среди трёх сотен строк и не
	// назначить показание на выключатель. Пусто — годится что угодно.
	Types []string `json:"types,omitempty"`
}

// Accepts сообщает, годится ли элемент такого типа под роль.
func (r Role) Accepts(elementType string) bool {
	if len(r.Types) == 0 {
		return true
	}
	for _, t := range r.Types {
		if t == elementType {
			return true
		}
	}
	return false
}

// LinkSpec — описание одной связки в шаблоне.
type LinkSpec struct {
	Name      string `json:"name"`
	Direction string `json:"direction"` // in | out
	Role      string `json:"role"`      // из какой роли берётся элемент
	Topic     string `json:"topic"`     // с подстановкой {{prefix}}

	Kind        string            `json:"kind,omitempty"`
	Extract     string            `json:"extract,omitempty"`
	ExtractPath string            `json:"extract_path,omitempty"`
	Values      map[string]string `json:"values,omitempty"`
	Encode      string            `json:"encode,omitempty"`
	Decode      string            `json:"decode,omitempty"`
	Unit        string            `json:"unit,omitempty"`
	Precision   *int              `json:"precision,omitempty"`
	Scale       float64           `json:"scale,omitempty"`
	QoS         byte              `json:"qos,omitempty"`
	Retain      bool              `json:"retain,omitempty"`

	// OnlyChanged по умолчанию включён: без отсева повторов одно и то же
	// значение уходит на каждом опросе.
	OnlyChanged *bool `json:"only_changed,omitempty"`

	// Pair помечает стороны одной двусторонней привязки. Связки с одинаковой
	// меткой заводятся и удаляются вместе.
	Pair string `json:"pair,omitempty"`
}

// Builtin возвращает встроенные шаблоны.
func Builtin() ([]Template, error) {
	entries, err := fs.ReadDir(templateFS, ".")
	if err != nil {
		return nil, fmt.Errorf("devtmpl: чтение шаблонов: %w", err)
	}

	out := make([]Template, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		body, err := templateFS.ReadFile(e.Name())
		if err != nil {
			return nil, fmt.Errorf("devtmpl: чтение %s: %w", e.Name(), err)
		}
		t, err := Parse(body)
		if err != nil {
			return nil, fmt.Errorf("devtmpl: %s: %w", e.Name(), err)
		}
		t.Key = strings.TrimSuffix(e.Name(), ".json")
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Find ищет встроенный шаблон по ключу.
func Find(key string) (Template, error) {
	all, err := Builtin()
	if err != nil {
		return Template{}, err
	}
	for _, t := range all {
		if t.Key == key {
			return t, nil
		}
	}
	return Template{}, fmt.Errorf("devtmpl: шаблон %q не найден", key)
}

// Parse разбирает шаблон и проверяет его целостность.
func Parse(data []byte) (Template, error) {
	var t Template
	if err := json.Unmarshal(data, &t); err != nil {
		return Template{}, fmt.Errorf("разбор шаблона: %w", err)
	}
	return t, t.Validate()
}

// Validate проверяет шаблон.
//
// Ошибка здесь дороже обычной: шаблон применяют один раз и потом доверяют ему,
// а неверная роль в нём означает связку без элемента — то есть значение,
// уходящее в никуда.
func (t Template) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("у шаблона нет названия")
	}
	if len(t.Links) == 0 {
		return fmt.Errorf("шаблон %q не содержит ни одной связки", t.Name)
	}

	roles := make(map[string]bool, len(t.Roles))
	for _, r := range t.Roles {
		if r.Key == "" {
			return fmt.Errorf("шаблон %q: у роли пустой ключ", t.Name)
		}
		if roles[r.Key] {
			return fmt.Errorf("шаблон %q: роль %q описана дважды", t.Name, r.Key)
		}
		roles[r.Key] = true
	}

	for i, l := range t.Links {
		if !roles[l.Role] {
			return fmt.Errorf("шаблон %q, связка %d: роль %q не описана", t.Name, i+1, l.Role)
		}
		if !strings.Contains(l.Topic, Placeholder) {
			return fmt.Errorf("шаблон %q, связка %d: в топике нет подстановки %s",
				t.Name, i+1, Placeholder)
		}
		switch link.Direction(l.Direction) {
		case link.In, link.Out:
		default:
			return fmt.Errorf("шаблон %q, связка %d: направление %q", t.Name, i+1, l.Direction)
		}
	}
	return nil
}

// Addr — адрес элемента умного дома.
type Addr struct {
	ID    uint16
	SubID uint8
}

// Apply разворачивает шаблон в набор связок.
//
// prefix — префикс топиков устройства, например
// "shellies/shellyswitch25-4022D8956527". assign — какой элемент назначен
// каждой роли; роли без назначения просто пропускаются, и связки под них не
// создаются. Так одним и тем же шаблоном покрывается и полностью заведённое
// устройство, и то, от которого нужен один канал.
func (t Template) Apply(prefix string, assign map[string]Addr) ([]link.Link, error) {
	prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return nil, fmt.Errorf("не задан префикс топиков устройства")
	}

	for _, r := range t.Roles {
		if _, ok := assign[r.Key]; !ok && r.Required {
			return nil, fmt.Errorf("не назначен элемент для роли «%s»", r.Title)
		}
	}

	out := make([]link.Link, 0, len(t.Links))
	for _, spec := range t.Links {
		addr, ok := assign[spec.Role]
		if !ok {
			continue
		}

		onlyChanged := true
		if spec.OnlyChanged != nil {
			onlyChanged = *spec.OnlyChanged
		}

		l := link.Link{
			Name:        spec.Name,
			Enabled:     true,
			Direction:   link.Direction(spec.Direction),
			Topic:       strings.ReplaceAll(spec.Topic, Placeholder, prefix),
			QoS:         spec.QoS,
			Retain:      spec.Retain,
			Kind:        spec.Kind,
			Extract:     spec.Extract,
			ExtractPath: spec.ExtractPath,
			Values:      spec.Values,
			Scale:       spec.Scale,
			TargetID:    addr.ID,
			TargetSubID: addr.SubID,
			Encode:      spec.Encode,
			Decode:      spec.Decode,
			Unit:        spec.Unit,
			Precision:   spec.Precision,
			OnlyChanged: onlyChanged,
		}.Normalize()

		if err := l.Validate(); err != nil {
			return nil, fmt.Errorf("связка «%s»: %w", spec.Name, err)
		}
		out = append(out, l)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("не назначено ни одного элемента")
	}
	return out, nil
}

// PairGroups перечисляет метки пар в порядке появления. Нужны хранилищу:
// связки с одной меткой заводятся и удаляются вместе.
func (t Template) PairGroups() []string {
	seen := make(map[string]bool)
	var out []string
	for _, l := range t.Links {
		if l.Pair == "" || seen[l.Pair] {
			continue
		}
		seen[l.Pair] = true
		out = append(out, l.Pair)
	}
	return out
}

// PairOf возвращает метку пары для связки под номером i.
func (t Template) PairOf(i int) string {
	if i < 0 || i >= len(t.Links) {
		return ""
	}
	return t.Links[i].Pair
}
