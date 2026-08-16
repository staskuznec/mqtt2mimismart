package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/staskuznec/mqtt2mimismart/internal/devtmpl"
	"github.com/staskuznec/mqtt2mimismart/internal/logic"
)

// Заведение элементов под профиль.
//
// Профиль знает, какие показания у устройства есть и какой формы значение у
// каждого. Всё, чего ему не хватает, — адресов: их выдаёт умный дом, и до сих
// пор человек придумывал их сам, по одному на роль, следя, чтобы не наступить
// на занятый. На инверторе с полусотней ролей это работа на вечер, и ошибка в
// ней тихая: связка пишет в чужой элемент.
//
// Записать logic.xml шлюз не может — файл принадлежит серверу умного дома, и
// юнит открывает его только на чтение. Поэтому шлюз готовит текст, а человек
// вставляет его через MimiSetup. Зато адреса шлюз потом помнит и подставляет
// элементы в роли сам.

type elementsNewData struct {
	Title, Nav string
	Error      string

	Templates []devtmpl.Item
	Selected  devtmpl.Template
	HasChoice bool

	Modules  []uint16 // модули, которые уже есть в logic.xml
	Module   uint16
	Area     string
	Areas    []string
	FromSub  uint8
	Prefix   string // префикс топиков: из него собираются комментарии
	NamePrfx string // приставка к именам элементов

	// Результат.
	XML   string
	Items []genRow
	Link  string // адрес формы устройства с уже подставленными ролями
}

// genRow — строка таблицы «роль → элемент».
type genRow struct {
	Role  string
	Title string
	Addr  string
	Name  string
	Kind  string
	Topic string
}

func (s *server) pageElementsNew(w http.ResponseWriter, r *http.Request) {
	data := s.elementsNewBase(r)
	s.render(w, "elements_new", data)
}

// elementsNewBase собирает всё, что нужно форме и до, и после генерации.
func (s *server) elementsNewBase(r *http.Request) elementsNewData {
	data := elementsNewData{Title: "Завести элементы", Nav: "elements"}

	if dir := s.db.TemplateDir(); dir != nil {
		if list, err := dir.List(); err == nil {
			for _, it := range list {
				if it.Error == "" {
					data.Templates = append(data.Templates, it)
				}
			}
		}
	}

	house := s.house()
	data.Modules = house.ModuleIDs()
	data.Areas = house.Areas()

	q := r.URL.Query()
	if r.Method == http.MethodPost {
		q = r.PostForm
	}

	data.Area = trim(q.Get("area"))
	data.Prefix = trim(q.Get("prefix"))
	data.NamePrfx = trim(q.Get("name_prefix"))

	if key := trim(q.Get("template")); key != "" {
		if t, err := s.db.Template(r.Context(), key); err == nil {
			data.Selected, data.HasChoice = t, true
		}
	}

	if v := trim(q.Get("module")); v != "" {
		if id, err := strconv.ParseUint(v, 10, 16); err == nil {
			data.Module = uint16(id)
		}
	} else if len(data.Modules) > 0 {
		data.Module = data.Modules[0]
	}

	if v := trim(q.Get("from_sub")); v != "" {
		if sub, err := strconv.ParseUint(v, 10, 8); err == nil {
			data.FromSub = uint8(sub)
		}
	} else if data.Module != 0 {
		data.FromSub = house.NextFreeSubID(data.Module)
	}

	return data
}

// genElements собирает разметку под выбранные роли.
func (s *server) genElements(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "форма не разобрана", http.StatusBadRequest)
		return
	}

	data := s.elementsNewBase(r)
	if !data.HasChoice {
		data.Error = "не выбран профиль"
		s.render(w, "elements_new", data)
		return
	}
	if data.Area == "" {
		data.Error = "не указана область: в неё попадут заведённые элементы"
		s.render(w, "elements_new", data)
		return
	}
	if data.Module == 0 {
		data.Error = "не указан модуль (CM), на котором заводить элементы"
		s.render(w, "elements_new", data)
		return
	}

	// Роли, отмеченные галочками. Порядок берём из профиля, а не из формы:
	// на экране элементы должны идти так же, как показания в устройстве.
	chosen := make(map[string]bool)
	for _, key := range r.PostForm["role"] {
		chosen[key] = true
	}

	roles := make([]devtmpl.Role, 0, len(data.Selected.Roles))
	for _, role := range data.Selected.Roles {
		if chosen[role.Key] {
			roles = append(roles, role)
		}
	}
	if len(roles) == 0 {
		data.Error = "не отмечено ни одной роли"
		s.render(w, "elements_new", data)
		return
	}

	house := s.house()
	subs, err := house.FreeSubIDs(data.Module, len(roles), data.FromSub)
	if err != nil {
		data.Error = err.Error()
		s.render(w, "elements_new", data)
		return
	}

	// Топик роли — для комментария в разметке: по нему через полгода видно,
	// откуда элемент получает значение.
	topics := make(map[string]string, len(data.Selected.Links))
	for _, l := range data.Selected.Links {
		if _, ok := topics[l.Role]; !ok {
			topics[l.Role] = topicOf(l.Topic, data.Prefix)
		}
	}

	items := make([]logic.NewItem, 0, len(roles))
	params := url.Values{}
	params.Set("template", data.Selected.Key)
	if data.Prefix != "" {
		params.Set("prefix", data.Prefix)
	}

	for i, role := range roles {
		item := logic.ItemFor(data.Module, subs[i],
			elementName(data.NamePrfx, role.Title), role.Form, topics[role.Key])
		item.Dim = logic.DimFor(role.Title)
		items = append(items, item)

		data.Items = append(data.Items, genRow{
			Role: role.Key, Title: role.Title, Addr: item.Addr(),
			Name: item.Name, Kind: item.SubType, Topic: item.Comment,
		})
		params.Set("role_"+role.Key, item.Addr())
	}

	data.XML = logic.RenderArea(data.Area, items)
	data.Link = "devices/new?" + params.Encode()
	s.render(w, "elements_new", data)
}

// house отдаёт разобранный logic.xml.
func (s *server) house() logic.House {
	if s.status.Elements == nil {
		return logic.House{}
	}
	return logic.House{Elements: s.status.Elements()}
}

// elementName собирает имя элемента из приставки и названия роли.
//
// Единицы из названия убираем: в приложении они и так подписаны атрибутом dim,
// а «Заряд АКБ, %» рядом с «87 %» читается как заикание.
func elementName(prefix, title string) string {
	name := title
	if i := strings.Index(name, ", "); i > 0 {
		name = name[:i]
	}
	if open := strings.Index(name, " ("); open > 0 {
		name = name[:open]
	}
	if prefix != "" {
		name = prefix + " " + name
	}
	return name
}

// topicOf подставляет префикс устройства в топик связки.
func topicOf(topic, prefix string) string {
	if prefix == "" {
		// Без префикса подстановка только мешает: комментарий должен читаться,
		// а не показывать шаблонную скобку.
		return strings.TrimPrefix(strings.ReplaceAll(topic, devtmpl.Placeholder, ""), "/")
	}
	return strings.ReplaceAll(topic, devtmpl.Placeholder, strings.TrimRight(prefix, "/"))
}
