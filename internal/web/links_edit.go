package web

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/staskuznec/mqtt2mimismart/internal/link"
	"github.com/staskuznec/mqtt2mimismart/internal/logic"
	"github.com/staskuznec/mqtt2mimismart/internal/store"
)

// linkFormData — всё, что нужно форме связки.
type linkFormData struct {
	Title, Nav string
	Link       link.Link
	New        bool
	Error      string

	Elements []elementOption
	Preview  *previewResult

	// Values показывается таблицей значений в виде «строка на пару»: править
	// JSON руками в веб-форме — издевательство.
	ValuesText string
}

// elementOption — элемент умного дома в выпадающем списке.
type elementOption struct {
	Addr     string
	Label    string // «Прихожая → Свет (lamp)»
	Form     string // подсказка формы значения по типу
	Selected bool
}

// previewResult — результат пробы: что уйдёт на провод.
type previewResult struct {
	OK      bool
	Value   string
	Hex     string
	Kind    string
	Clamped bool
	Error   string
}

func (s *server) pageLinkForm(w http.ResponseWriter, r *http.Request) {
	data := linkFormData{Title: "Связка", Nav: "links"}

	if id := r.PathValue("id"); id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			http.Error(w, "неверный идентификатор связки", http.StatusBadRequest)
			return
		}
		l, err := s.db.Link(r.Context(), n)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		data.Link = l
	} else {
		data.New = true
		// Топик подставляется из раздела «Топики»: связка заводится нажатием
		// рядом с тем, что реально ходит по шине, а не набором руками.
		data.Link = link.Link{
			Enabled:     true,
			Direction:   link.In,
			Topic:       r.URL.Query().Get("topic"),
			Extract:     link.ExtractRaw,
			Encode:      link.EncodeByte,
			Decode:      link.DecodeLamp,
			Kind:        link.KindCommand,
			OnlyChanged: true,
		}
	}

	data.ValuesText = valuesToText(data.Link.Values)
	data.Elements = s.elementOptions(data.Link.Addr())
	s.render(w, "link_form", data)
}

// elementOptions собирает список элементов для выбора.
func (s *server) elementOptions(selected string) []elementOption {
	if s.status.Elements == nil {
		return nil
	}

	elements := s.status.Elements()
	out := make([]elementOption, 0, len(elements))
	for _, e := range elements {
		label := e.Name
		if e.Area != "" {
			label = e.Area + " → " + e.Name
		}
		if e.Type != "" {
			label += " (" + e.Type + ")"
		}
		out = append(out, elementOption{
			Addr:     e.Addr(),
			Label:    label,
			Form:     e.Form(),
			Selected: e.Addr() == selected,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// linkFromForm собирает связку из полей формы.
func linkFromForm(r *http.Request) (link.Link, error) {
	l := link.Link{
		Name:        trim(r.PostFormValue("name")),
		Enabled:     r.PostFormValue("enabled") != "",
		Direction:   link.Direction(r.PostFormValue("direction")),
		Topic:       trim(r.PostFormValue("topic")),
		Extract:     r.PostFormValue("extract"),
		ExtractPath: trim(r.PostFormValue("extract_path")),
		Encode:      r.PostFormValue("encode"),
		Decode:      r.PostFormValue("decode"),
		Kind:        r.PostFormValue("kind"),
		Unit:        r.PostFormValue("unit"),
		OnlyChanged: r.PostFormValue("only_changed") != "",
		Retain:      r.PostFormValue("retain") != "",
	}

	if id := trim(r.PostFormValue("id")); id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return l, errors.New("неверный идентификатор связки")
		}
		l.ID = n
	}

	addr := trim(r.PostFormValue("target"))
	if addr == "" {
		return l, errors.New("не выбран элемент умного дома")
	}
	id, subID, err := parseAddr(addr)
	if err != nil {
		return l, err
	}
	l.TargetID, l.TargetSubID = id, subID

	if qos := trim(r.PostFormValue("qos")); qos != "" {
		n, err := strconv.ParseUint(qos, 10, 8)
		if err != nil || n > 2 {
			return l, errors.New("QoS должен быть 0, 1 или 2")
		}
		l.QoS = byte(n)
	}

	// Пустое поле точности означает «без округления», и это не то же самое,
	// что ноль: ноль округляет до целого.
	if p := trim(r.PostFormValue("precision")); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return l, errors.New("знаков после запятой: ожидалось неотрицательное число")
		}
		l.Precision = &n
	}

	if scale := trim(r.PostFormValue("scale")); scale != "" {
		v, err := strconv.ParseFloat(scale, 64)
		if err != nil {
			return l, errors.New("множитель: ожидалось число")
		}
		l.Scale = v
	}
	if offset := trim(r.PostFormValue("offset")); offset != "" {
		v, err := strconv.ParseFloat(offset, 64)
		if err != nil {
			return l, errors.New("смещение: ожидалось число")
		}
		l.Offset = v
	}

	l.Values, err = valuesFromText(r.PostFormValue("values"))
	if err != nil {
		return l, err
	}
	return l, nil
}

func (s *server) saveLink(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "форма не разобрана", http.StatusBadRequest)
		return
	}

	l, err := linkFromForm(r)
	if err == nil {
		if l.ID == 0 {
			_, err = s.db.CreateLink(r.Context(), l)
		} else {
			err = s.db.UpdateLink(r.Context(), l)
		}
	}
	if err != nil {
		// Форму отдаём обратно с введённым: терять заполненное из-за одной
		// опечатки — верный способ отбить желание пользоваться.
		s.render(w, "link_form", linkFormData{
			Title: "Связка", Nav: "links",
			Link: l, New: l.ID == 0, Error: err.Error(),
			ValuesText: r.PostFormValue("values"),
			Elements:   s.elementOptions(l.Addr()),
		})
		return
	}

	s.reload(r)
	http.Redirect(w, r, "/links", http.StatusSeeOther)
}

func (s *server) toggleLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "неверный идентификатор связки", http.StatusBadRequest)
		return
	}
	l, err := s.db.Link(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := s.db.SetLinkEnabled(r.Context(), id, !l.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.reload(r)
	http.Redirect(w, r, "/links", http.StatusSeeOther)
}

func (s *server) deleteLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "неверный идентификатор связки", http.StatusBadRequest)
		return
	}
	if err := s.db.DeleteLink(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.reload(r)
	http.Redirect(w, r, "/links", http.StatusSeeOther)
}

// previewLink показывает, во что превратится нагрузка и какие байты уйдут.
//
// Считает тот же самый код, который потом и отправит значение, — поэтому
// расхождение между пробой и работой невозможно. Ради этого проба и живёт на
// сервере, а не в браузере.
func (s *server) previewLink(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "форма не разобрана", http.StatusBadRequest)
		return
	}

	sample := r.PostFormValue("sample")
	l, err := linkFromForm(r)
	if err != nil {
		s.render(w, "preview", previewResult{Error: err.Error()})
		return
	}

	var result previewResult
	if l.Direction == link.Out {
		payload, err := l.ToPayload([]byte(sample))
		if err != nil {
			result.Error = err.Error()
		} else {
			result.OK, result.Value, result.Kind = true, payload, "нагрузка в топик"
		}
	} else {
		wire, err := l.ToWire([]byte(sample))
		if err != nil {
			result.Error = err.Error()
		} else {
			result.OK = true
			result.Value, result.Hex = wire.Text, wire.Hex()
			result.Kind, result.Clamped = wire.Kind, wire.Clamped
		}
	}
	s.render(w, "preview", result)
}

// reload просит движок перечитать связки. Ошибку только пишем в журнал:
// связка уже сохранена, и отвечать пользователю ошибкой было бы неправдой.
func (s *server) reload(r *http.Request) {
	if s.status.Reload == nil {
		return
	}
	if err := s.status.Reload(r.Context()); err != nil {
		s.log.Error("перечитывание связок", "err", err)
	}
}

// ---------------------------------------------------------------- Элементы

type elementsData struct {
	Title, Nav string
	Elements   []logic.Element
}

func (s *server) pageElements(w http.ResponseWriter, _ *http.Request) {
	data := elementsData{Title: "Элементы", Nav: "elements"}
	if s.status.Elements != nil {
		data.Elements = s.status.Elements()
	}
	s.render(w, "elements", data)
}

// ---------------------------------------------------------------- Общее

// valuesToText разворачивает таблицу значений в строки «ключ = значение».
func valuesToText(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(" = ")
		b.WriteString(values[k])
		b.WriteString("\n")
	}
	return b.String()
}

// valuesFromText разбирает таблицу значений из строк «ключ = значение».
func valuesFromText(text string) (map[string]string, error) {
	values := make(map[string]string)
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, errors.New("таблица значений, строка " + strconv.Itoa(i+1) +
				": ожидался вид «ключ = значение»")
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if len(values) == 0 {
		return nil, nil
	}
	return values, nil
}

// parseAddr разбирает адрес элемента "563:57".
func parseAddr(s string) (uint16, uint8, error) {
	idStr, subStr, found := strings.Cut(s, ":")
	if !found {
		return 0, 0, errors.New("адрес элемента: ожидался вид id:subid")
	}
	id, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 16)
	if err != nil {
		return 0, 0, errors.New("адрес элемента: id должен быть числом 0..65535")
	}
	sub, err := strconv.ParseUint(strings.TrimSpace(subStr), 10, 8)
	if err != nil {
		return 0, 0, errors.New("адрес элемента: subid должен быть числом 0..255")
	}
	return uint16(id), uint8(sub), nil
}
