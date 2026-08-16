package web

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/staskuznec/mqtt2mimismart/internal/devtmpl"
	"github.com/staskuznec/mqtt2mimismart/internal/logic"
	"github.com/staskuznec/mqtt2mimismart/internal/store"
)

// ---------------------------------------------------------------- Устройства

type devicesData struct {
	Title, Nav string
	Devices    []deviceRow
	Error      string
}

type deviceRow struct {
	store.Device
	Links   int
	Enabled int
}

func (s *server) pageDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.db.Devices(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := devicesData{Title: "Устройства", Nav: "devices"}
	for _, d := range devices {
		row := deviceRow{Device: d}
		if links, err := s.db.LinksByDevice(r.Context(), d.ID); err == nil {
			row.Links = len(links)
			for _, l := range links {
				if l.Enabled {
					row.Enabled++
				}
			}
		}
		data.Devices = append(data.Devices, row)
	}
	s.render(w, "devices", data)
}

// deviceFormData — форма развёртывания шаблона на устройстве.
type deviceFormData struct {
	Title, Nav string
	Error      string

	Templates []devtmpl.Item
	Selected  devtmpl.Template
	HasChoice bool // шаблон выбран, показываем роли

	// DeviceID не ноль — правим уже заведённое устройство: тот же визард,
	// только назначения ролей подставлены из того, что развёрнуто.
	DeviceID int64

	Name     string
	Prefix   string
	Prefixes []prefixOption // что видно на шине
	Elements []elementOption

	// Roles — роли шаблона вместе с подходящими им элементами: список сужен
	// по типам, чтобы не искать лампу среди трёх сотен строк.
	Roles []roleOption

	// Заведение элементов прямо здесь: под роль, которой в умном доме ещё
	// нечего назначить, шлюз подберёт свободный адрес и соберёт разметку.
	Modules []uint16 // модули (CM), которые уже есть в logic.xml
	Areas   []string
	Module  uint16
	Area    string
	FromSub uint8

	// XML — разметка для logic.xml, собранная при сохранении, и что в неё
	// вошло. Показывается после развёртывания: вставить её должен человек.
	XML     string
	Created []createdRow
}

// createdRow — элемент, заведённый под роль.
type createdRow struct {
	Title string
	Name  string
	Addr  string
	Kind  string
}

// roleOption — роль с уже отобранными под неё элементами.
type roleOption struct {
	devtmpl.Role
	Elements []elementOption
	Narrowed bool   // список сужен по типу, есть что показать целиком
	Selected string // назначенный элемент при правке устройства
}

// prefixOption — предполагаемый префикс устройства, собранный из снифера.
type prefixOption struct {
	Prefix string
	Topics int
	Known  bool // устройство с таким префиксом уже заведено
}

func (s *server) pageDeviceForm(w http.ResponseWriter, r *http.Request) {
	data := deviceFormData{Title: "Новое устройство", Nav: "devices"}

	dir := s.db.TemplateDir()
	if dir == nil {
		http.Error(w, "каталог шаблонов не настроен", http.StatusInternalServerError)
		return
	}
	list, err := dir.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Битые файлы в выборе модели не показываем: развернуть их всё равно
	// нельзя, а причина видна на странице «Шаблоны».
	for _, it := range list {
		if it.Error == "" {
			data.Templates = append(data.Templates, it)
		}
	}

	data.Prefix = r.URL.Query().Get("prefix")
	data.Prefixes = s.prefixOptions(r)
	data.Elements = s.elementOptions("")

	house := s.house()
	data.Modules = house.ModuleIDs()
	if len(data.Modules) > 0 {
		data.Module = data.Modules[0]
		data.FromSub = house.NextFreeSubID(data.Module)
	}
	data.Areas = house.Areas()

	// Правка заведённого устройства: тот же визард с подставленными
	// назначениями. Пересоздавать устройство ради переназначенной роли —
	// значит терять имя, историю и связки, заведённые к нему руками.
	if id := r.PathValue("id"); id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			http.Error(w, "неверный идентификатор устройства", http.StatusBadRequest)
			return
		}
		d, err := s.db.Device(r.Context(), n)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		data.Title = "Настройка устройства"
		data.DeviceID, data.Name, data.Prefix = d.ID, d.Name, d.TopicPrefix

		key := d.Template
		if q := r.URL.Query().Get("template"); q != "" {
			key = q // шаблон переключили прямо в форме
		}
		t, err := s.db.Template(r.Context(), key)
		if err != nil {
			data.Error = err.Error()
			s.render(w, "device_form", data)
			return
		}
		data.Selected, data.HasChoice = t, true

		assign, err := s.db.Assignment(r.Context(), d.ID, t)
		if err != nil {
			data.Error = err.Error()
		}
		data.Roles = s.roleOptions(t, data.Elements, assignFromQuery(r, t, assign))
		s.render(w, "device_form", data)
		return
	}

	if key := r.URL.Query().Get("template"); key != "" {
		t, err := s.db.Template(r.Context(), key)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Selected, data.HasChoice = t, true
			data.Name = t.Name
		}
	}
	data.Roles = s.roleOptions(data.Selected, data.Elements,
		assignFromQuery(r, data.Selected, nil))

	s.render(w, "device_form", data)
}

// assignFromQuery достаёт назначения ролей из адреса страницы.
//
// Так форма открывается уже заполненной после заведения элементов: страница
// «Завести элементы» знает, какой адрес какой роли достался, и передаёт это
// сюда. Иначе человеку пришлось бы разложить полсотни свежих адресов по ролям
// руками — ровно ту работу, ради избавления от которой всё и делалось.
//
// Сохранённое назначение сильнее: устройство уже настроено, и переписать его
// ссылкой было бы неожиданно.
func assignFromQuery(r *http.Request, t devtmpl.Template,
	saved map[string]devtmpl.Addr) map[string]devtmpl.Addr {

	q := r.URL.Query()
	out := make(map[string]devtmpl.Addr, len(t.Roles))
	for key, addr := range saved {
		out[key] = addr
	}

	for _, role := range t.Roles {
		if _, ok := out[role.Key]; ok {
			continue
		}
		value := trim(q.Get("role_" + role.Key))
		if value == "" {
			continue
		}
		id, subID, err := parseAddr(value)
		if err != nil {
			continue // мусор в адресе странице не важен: роль просто останется пустой
		}
		out[role.Key] = devtmpl.Addr{ID: id, SubID: subID}
	}
	return out
}

// roleOptions сужает список элементов под каждую роль.
//
// Роль знает, какие типы ей годятся: под канал реле нужна лампа, под
// температуру — датчик. Показывать при этом все три сотни элементов дома
// значит заставлять искать глазами и ошибаться.
func (s *server) roleOptions(t devtmpl.Template, all []elementOption,
	assign map[string]devtmpl.Addr) []roleOption {

	out := make([]roleOption, 0, len(t.Roles))
	for _, role := range t.Roles {
		opt := roleOption{Role: role}
		if addr, ok := assign[role.Key]; ok {
			opt.Selected = fmt.Sprintf("%d:%d", addr.ID, addr.SubID)
		}
		for _, e := range all {
			if role.Accepts(e.Type) {
				opt.Elements = append(opt.Elements, e)
			}
		}
		// Если под роль не нашлось ничего, показываем всё: пустой список —
		// тупик, а неточный тип в logic.xml встречается сплошь и рядом.
		if len(opt.Elements) == 0 {
			opt.Elements = all
		} else {
			opt.Narrowed = len(opt.Elements) < len(all)
		}
		out = append(out, opt)
	}
	return out
}

// prefixOptions собирает предполагаемые префиксы устройств из того, что реально
// ходит по шине.
//
// Префикс — это первые два уровня топика: у Shelly это "shellies/<id>", и
// такая же двухуровневая схема у большинства прошивок. Набирать его руками
// незачем: снифер уже знает всё, что подключено.
func (s *server) prefixOptions(r *http.Request) []prefixOption {
	if s.status.Topics == nil {
		return nil
	}

	counts := make(map[string]int)
	for _, t := range s.status.Topics() {
		parts := strings.Split(t.Topic, "/")
		if len(parts) < 3 {
			continue // короткие топики устройством не пахнут
		}
		counts[parts[0]+"/"+parts[1]]++
	}

	known := make(map[string]bool)
	if devices, err := s.db.Devices(r.Context()); err == nil {
		for _, d := range devices {
			known[d.TopicPrefix] = true
		}
	}

	out := make([]prefixOption, 0, len(counts))
	for prefix, n := range counts {
		out = append(out, prefixOption{Prefix: prefix, Topics: n, Known: known[prefix]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Known != out[j].Known {
			return !out[i].Known // ещё не заведённые сверху
		}
		return out[i].Topics > out[j].Topics
	})
	return out
}

func (s *server) applyTemplate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "форма не разобрана", http.StatusBadRequest)
		return
	}

	key := r.PostFormValue("template")
	prefix := trim(r.PostFormValue("prefix"))
	name := trim(r.PostFormValue("name"))

	var deviceID int64
	if id := trim(r.PostFormValue("device_id")); id != "" {
		deviceID, _ = strconv.ParseInt(id, 10, 64)
	}

	fail := func(msg string, assign map[string]devtmpl.Addr) {
		house := s.house()
		data := deviceFormData{
			Title: "Новое устройство", Nav: "devices",
			Error: msg, Name: name, Prefix: prefix, DeviceID: deviceID,
			Prefixes: s.prefixOptions(r),
			Elements: s.elementOptions(""),
			Modules:  house.ModuleIDs(),
			Areas:    house.Areas(),
			Module:   moduleFromForm(r),
			Area:     trim(r.PostFormValue("area")),
		}
		if deviceID != 0 {
			data.Title = "Настройка устройства"
		}
		if dir := s.db.TemplateDir(); dir != nil {
			if list, err := dir.List(); err == nil {
				for _, it := range list {
					if it.Error == "" {
						data.Templates = append(data.Templates, it)
					}
				}
			}
		}
		if t, err := s.db.Template(r.Context(), key); err == nil {
			data.Selected, data.HasChoice = t, true
			data.Roles = s.roleOptions(t, data.Elements, assign)
		}
		s.render(w, "device_form", data)
	}

	tmpl, err := s.db.Template(r.Context(), key)
	if err != nil {
		fail(err.Error(), nil)
		return
	}

	// Роли без выбранного элемента просто пропускаются: не нужны показания —
	// не назначайте их. Значение "new" означает, что элемента в умном доме
	// ещё нет и его надо завести.
	assign := make(map[string]devtmpl.Addr)
	var create []devtmpl.Role

	for _, role := range tmpl.Roles {
		addr := trim(r.PostFormValue("role_" + role.Key))
		switch addr {
		case "":
			continue
		case createValue:
			create = append(create, role)
			continue
		}
		id, subID, err := parseAddr(addr)
		if err != nil {
			fail("роль «"+role.Title+"»: "+err.Error(), assign)
			return
		}
		assign[role.Key] = devtmpl.Addr{ID: id, SubID: subID}
	}

	// Заведение элементов: адреса подбираются из свободных на выбранном
	// модуле. Занятые пропускаются — записать показание в чужой элемент хуже,
	// чем не завести его вовсе.
	var items []logic.NewItem
	if len(create) > 0 {
		module := moduleFromForm(r)
		area := trim(r.PostFormValue("area"))
		switch {
		case module == 0:
			fail("не выбран модуль (CM), на котором заводить элементы", assign)
			return
		case area == "":
			fail("не указана область: в неё попадут заведённые элементы", assign)
			return
		}

		house := s.house()
		subs, err := house.FreeSubIDs(module, len(create), subFromForm(r, house, module))
		if err != nil {
			fail(err.Error(), assign)
			return
		}

		topics := make(map[string]string, len(tmpl.Links))
		for _, l := range tmpl.Links {
			if _, ok := topics[l.Role]; !ok {
				topics[l.Role] = topicOf(l.Topic, prefix)
			}
		}

		for i, role := range create {
			item := logic.ItemFor(module, subs[i],
				elementName(name, role.Title), role.Form, topics[role.Key])
			item.Dim = logic.DimFor(role.Title)
			items = append(items, item)
			assign[role.Key] = devtmpl.Addr{ID: item.ID, SubID: item.SubID}
		}
	}

	device := store.Device{ID: deviceID, Name: name, TopicPrefix: prefix}
	if deviceID != 0 {
		err = s.db.ReapplyTemplate(r.Context(), device, tmpl, assign)
	} else {
		_, err = s.db.ApplyTemplate(r.Context(), device, tmpl, assign)
	}
	if err != nil {
		fail(err.Error(), assign)
		return
	}

	s.reload(r)

	if len(items) > 0 {
		// Связки уже заведены и ждут своих элементов; вставить разметку в
		// logic.xml должен человек. Поэтому не редирект на список устройств, а
		// страница с готовым куском и порядком действий.
		area := trim(r.PostFormValue("area"))
		data := deviceCreatedData{
			Title: "Элементы заведены", Nav: "devices",
			Device: name, Area: area, XML: logic.RenderArea(area, items),
		}
		for i, item := range items {
			data.Created = append(data.Created, createdRow{
				Title: create[i].Title, Name: item.Name,
				Addr: item.Addr(), Kind: item.SubType,
			})
		}
		s.render(w, "device_created", data)
		return
	}

	s.redirect(w, r, "/devices")
}

// deviceCreatedData — что показать после развёртывания с заведением элементов.
type deviceCreatedData struct {
	Title, Nav string
	Device     string
	Area       string
	XML        string
	Created    []createdRow
}

// createValue — значение в списке элементов, означающее «завести новый».
const createValue = "new"

// moduleFromForm читает выбранный модуль.
func moduleFromForm(r *http.Request) uint16 {
	v := trim(r.PostFormValue("module"))
	if v == "" {
		return 0
	}
	id, err := strconv.ParseUint(v, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(id)
}

// subFromForm читает, с какого адреса начинать. Пусто — со следующего за
// последним занятым на этом модуле.
func subFromForm(r *http.Request, house logic.House, module uint16) uint8 {
	v := trim(r.PostFormValue("from_sub"))
	if v == "" {
		return house.NextFreeSubID(module)
	}
	sub, err := strconv.ParseUint(v, 10, 8)
	if err != nil {
		return house.NextFreeSubID(module)
	}
	return uint8(sub)
}

func (s *server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "неверный идентификатор устройства", http.StatusBadRequest)
		return
	}
	if err := s.db.DeleteDevice(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.reload(r)
	s.redirect(w, r, "/devices")
}

// ---------------------------------------------------------------- Шаблоны

type templatesData struct {
	Title, Nav string
	Templates  []devtmpl.Item
	Dir        string
	Hidden     bool // каталог спрятан в служебном месте, класть туда файлы неудобно
	Error      string
	Saved      string
}

func (s *server) pageTemplates(w http.ResponseWriter, r *http.Request) {
	dir := s.db.TemplateDir()
	if dir == nil {
		http.Error(w, "каталог шаблонов не настроен", http.StatusInternalServerError)
		return
	}

	list, err := dir.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Каталог по умолчанию лежит рядом с базой, в /var/lib: шлюз там пишет, но
	// человеку класть туда файлы неудобно и непривычно. Значит служба ещё не
	// знает про отдельный каталог — обновление через веб юнит не правит.
	hidden := strings.HasPrefix(dir.Path(), "/var/lib/")

	s.render(w, "templates", templatesData{
		Title: "Профили", Nav: "templates",
		Templates: list, Dir: dir.Path(), Hidden: hidden,
		Saved: r.URL.Query().Get("saved"),
	})
}

// templateEditData — редактор одного шаблона.
type templateEditData struct {
	Title, Nav string
	Key        string
	Body       string
	New        bool
	Bundled    bool
	Error      string
	Saved      bool
}

func (s *server) pageTemplateEdit(w http.ResponseWriter, r *http.Request) {
	dir := s.db.TemplateDir()
	if dir == nil {
		http.Error(w, "каталог шаблонов не настроен", http.StatusInternalServerError)
		return
	}

	key := r.PathValue("key")
	data := templateEditData{
		Title: "Профиль", Nav: "templates", Key: key,
		Saved: r.URL.Query().Get("saved") == "1",
	}

	if key == "new" {
		data.New, data.Key = true, ""
		data.Body = newTemplateSkeleton
		s.render(w, "template_edit", data)
		return
	}

	body, err := dir.Raw(key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	data.Body = string(body)

	if list, err := dir.List(); err == nil {
		for _, it := range list {
			if it.Key == key {
				data.Bundled = it.Bundled
			}
		}
	}
	s.render(w, "template_edit", data)
}

func (s *server) saveTemplate(w http.ResponseWriter, r *http.Request) {
	dir := s.db.TemplateDir()
	if dir == nil {
		http.Error(w, "каталог шаблонов не настроен", http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "форма не разобрана", http.StatusBadRequest)
		return
	}

	key := trim(r.PostFormValue("key"))
	body := r.PostFormValue("body")

	fail := func(msg string) {
		s.render(w, "template_edit", templateEditData{
			Title: "Профиль", Nav: "templates",
			Key: key, Body: body, Error: msg, New: r.PostFormValue("new") == "1",
		})
	}

	if key == "" {
		fail("не задан ключ шаблона — по нему он опознаётся и так называется файл")
		return
	}
	if err := dir.Save(key, []byte(body)); err != nil {
		fail(err.Error())
		return
	}

	s.log.Info("шаблон сохранён", "key", key)
	s.redirect(w, r, "/templates?saved="+key)
}

func (s *server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	dir := s.db.TemplateDir()
	if dir == nil {
		http.Error(w, "каталог шаблонов не настроен", http.StatusInternalServerError)
		return
	}
	if err := dir.Delete(r.PathValue("key")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.redirect(w, r, "/templates")
}

// showTemplate отдаёт файл шаблона как есть — чтобы скачать или скопировать.
func (s *server) showTemplate(w http.ResponseWriter, r *http.Request) {
	dir := s.db.TemplateDir()
	if dir == nil {
		http.Error(w, "каталог шаблонов не настроен", http.StatusInternalServerError)
		return
	}
	body, err := dir.Raw(r.PathValue("key"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}

// newTemplateSkeleton — заготовка новой модели: пустой файл перед человеком
// хуже, чем рабочий пример, который остаётся поправить.
const newTemplateSkeleton = `{
  "name": "Моя модель",
  "model": "SHPLG-S",
  "note": "Чем эта модель отличается и что важно знать перед применением.",

  "roles": [
    {"key": "sw", "title": "Розетка", "form": "byte", "required": true,
     "types": ["lamp", "script"]},
    {"key": "power", "title": "Мощность, Вт", "form": "text", "types": ["virtual"]}
  ],

  "links": [
    {
      "name": "Состояние", "direction": "in", "role": "sw", "pair": "sw",
      "topic": "{{prefix}}/relay/0",
      "encode": "byte",
      "values": {"on": "1", "off": "0", "overpower": "1"}
    },
    {
      "name": "Команда", "direction": "out", "role": "sw", "pair": "sw",
      "topic": "{{prefix}}/relay/0/command",
      "kind": "command", "decode": "lamp", "qos": 1,
      "values": {"on": "on", "off": "off", "toggle": "toggle"}
    },
    {
      "name": "Мощность", "direction": "in", "role": "power",
      "topic": "{{prefix}}/relay/0/power",
      "encode": "text", "unit": " Вт", "precision": 1
    }
  ]
}
`
