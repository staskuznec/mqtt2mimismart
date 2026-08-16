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
	Topics   []string // что видно на шине, для подсказки в поле топика
	Preview  *previewResult

	// Both — двусторонняя привязка: одно действие в интерфейсе, две связки
	// под капотом. Поля второй стороны живут отдельно.
	Both         bool
	CommandTopic string
	ValuesOut    string
	Decode       string
	Kind         string
	QoS          byte
	Retain       bool

	// Values показывается таблицей значений в виде «строка на пару»: править
	// JSON руками в веб-форме — издевательство.
	ValuesText string
}

// elementOption — элемент умного дома в выпадающем списке.
type elementOption struct {
	Addr     string
	Label    string // «Прихожая → Свет (lamp)»
	Type     string // из logic.xml: lamp, temperature-sensor и прочие
	Form     string // подсказка формы значения по типу
	Area     string
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

		// Двусторонняя привязка правится целиком: открыли одну сторону —
		// видим обе, иначе легко изменить топик у одной и забыть про вторую.
		if l.PairID != 0 {
			pair, err := s.db.PairLinks(r.Context(), l.PairID)
			if err == nil {
				data.Both = true
				for _, p := range pair {
					if p.Direction == link.In {
						data.Link = p
					} else {
						data.CommandTopic = p.Topic
						data.ValuesOut = valuesToText(p.Values)
						data.Decode, data.Kind = p.Decode, p.Kind
						data.QoS, data.Retain = p.QoS, p.Retain
					}
				}
			}
		}
	} else {
		data.New = true
		// Топик подставляется из раздела «Топики»: связка заводится нажатием
		// рядом с тем, что реально ходит по шине, а не набором руками.
		topic := r.URL.Query().Get("topic")
		data.Link = link.Link{
			Enabled:     true,
			Direction:   link.In,
			Topic:       topic,
			Extract:     link.ExtractRaw,
			Encode:      link.EncodeByte,
			OnlyChanged: true,
		}
		data.Decode, data.Kind, data.QoS = link.DecodeLamp, link.KindCommand, 1
		// Командный топик у Shelly — тот же плюс "/command". Угадывать не
		// обязательно, но в девяти случаях из десяти это он и есть.
		if topic != "" {
			data.CommandTopic = topic + "/command"
		}
		// Готовые таблицы для самого частого случая — реле и лампы.
		data.ValuesText = "on = 1\noff = 0\noverpower = 1\n"
		data.ValuesOut = "on = on\noff = off\ntoggle = toggle\n"
	}

	if data.ValuesText == "" {
		data.ValuesText = valuesToText(data.Link.Values)
	}
	if data.Decode == "" {
		data.Decode, data.Kind = link.DecodeLamp, link.KindCommand
	}
	data.Elements = s.elementOptions(data.Link.Addr())
	data.Topics = s.knownTopics()
	s.render(w, "link_form", data)
}

// knownTopics отдаёт топики, реально ходящие по шине, — для подсказки в поле.
func (s *server) knownTopics() []string {
	if s.status.Topics == nil {
		return nil
	}
	seen := s.status.Topics()
	out := make([]string, 0, len(seen))
	for _, t := range seen {
		out = append(out, t.Topic)
	}
	sort.Strings(out)
	return out
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
			Type:     e.Type,
			Form:     e.Form(),
			Area:     e.Area,
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

	// Направление "both" разбирается отдельно: в модели его нет, там всегда
	// одна сторона.
	if l.Direction == "both" {
		l.Direction = link.In
	}

	if pair := trim(r.PostFormValue("pair_id")); pair != "" {
		if n, err := strconv.ParseInt(pair, 10, 64); err == nil {
			l.PairID = n
		}
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

	both := r.PostFormValue("direction") == "both"

	l, err := linkFromForm(r)

	// Форма про устройство не знает, а связка ему принадлежит. Без этого
	// правка выбрасывала бы её из карточки устройства в общий список — и
	// удаление устройства переставало бы её уносить.
	if err == nil && l.ID != 0 {
		if stored, e := s.db.Link(r.Context(), l.ID); e == nil {
			l.DeviceID = stored.DeviceID
		}
	}

	switch {
	case err != nil:
	case both:
		err = s.savePair(r, l)
	case l.ID == 0:
		_, err = s.db.CreateLink(r.Context(), l)
	default:
		err = s.db.UpdateLink(r.Context(), l)
	}
	if err != nil {
		// Форму отдаём обратно с введённым: терять заполненное из-за одной
		// опечатки — верный способ отбить желание пользоваться.
		s.render(w, "link_form", linkFormData{
			Title: "Связка", Nav: "links",
			Link: l, New: l.ID == 0, Error: err.Error(),
			ValuesText:   r.PostFormValue("values"),
			Elements:     s.elementOptions(l.Addr()),
			Both:         both,
			CommandTopic: r.PostFormValue("command_topic"),
			ValuesOut:    r.PostFormValue("values_out"),
			Decode:       r.PostFormValue("decode"),
			Kind:         r.PostFormValue("kind"),
		})
		return
	}

	s.reload(r)
	s.redirect(w, r, "/links")
}

// savePair собирает из одной формы обе стороны двусторонней привязки.
func (s *server) savePair(r *http.Request, in link.Link) error {
	in.Direction = link.In

	out := link.Link{
		ID:          in.PairID, // подставится ниже, если сторона уже есть
		Name:        in.Name,
		Enabled:     in.Enabled,
		Direction:   link.Out,
		Topic:       trim(r.PostFormValue("command_topic")),
		Kind:        r.PostFormValue("kind"),
		Decode:      r.PostFormValue("decode"),
		QoS:         in.QoS,
		Retain:      in.Retain,
		TargetID:    in.TargetID,
		TargetSubID: in.TargetSubID,
		OnlyChanged: in.OnlyChanged,
		DeviceID:    in.DeviceID,
	}

	values, err := valuesFromText(r.PostFormValue("values_out"))
	if err != nil {
		return err
	}
	out.Values = values

	// Правим существующую пару — находим вторую сторону по её идентификатору
	// и сохраняем её принадлежность устройству.
	out.ID = 0
	if in.PairID != 0 {
		pair, err := s.db.PairLinks(r.Context(), in.PairID)
		if err != nil {
			return err
		}
		for _, p := range pair {
			if p.Direction == link.Out {
				out.ID, out.DeviceID = p.ID, p.DeviceID
			}
		}
	}

	return s.db.SavePair(r.Context(), in, out)
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
	// Пара переключается целиком: половина привязки — это элемент, который
	// показывает состояние, но не управляет.
	if l.PairID != 0 {
		err = s.db.SetPairEnabled(r.Context(), l.PairID, !l.Enabled)
	} else {
		err = s.db.SetLinkEnabled(r.Context(), id, !l.Enabled)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.reload(r)
	s.redirect(w, r, "/links")
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
	s.redirect(w, r, "/links")
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
	Reloading  bool // идёт переподключение ради свежего logic.xml
}

func (s *server) pageElements(w http.ResponseWriter, r *http.Request) {
	data := elementsData{
		Title: "Элементы", Nav: "elements",
		Reloading: r.URL.Query().Get("reloading") != "",
	}
	if s.status.Elements != nil {
		data.Elements = s.status.Elements()
	}
	s.render(w, "elements", data)
}

// reloadElements перечитывает logic.xml.
//
// Описание дома приезжает один раз — в рукопожатии, — и пока соединение живо,
// сервер его не переприсылает. Добавленный в logic.xml элемент шлюз поэтому не
// видит: для него ничего не изменилось. Единственный способ получить свежее
// описание — поздороваться заново, что и делает переподключение.
func (s *server) reloadElements(w http.ResponseWriter, r *http.Request) {
	if s.status.Reconfigure != nil {
		s.status.Reconfigure()
	}
	http.Redirect(w, r, "elements?reloading=1", http.StatusSeeOther)
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
