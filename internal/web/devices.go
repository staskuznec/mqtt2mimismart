package web

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/staskuznec/mqtt2mimismart/internal/devtmpl"
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

	Templates []store.TemplateInfo
	Selected  devtmpl.Template
	HasChoice bool // шаблон выбран, показываем роли

	Name     string
	Prefix   string
	Prefixes []prefixOption // что видно на шине
	Elements []elementOption
}

// prefixOption — предполагаемый префикс устройства, собранный из снифера.
type prefixOption struct {
	Prefix string
	Topics int
	Known  bool // устройство с таким префиксом уже заведено
}

func (s *server) pageDeviceForm(w http.ResponseWriter, r *http.Request) {
	data := deviceFormData{Title: "Новое устройство", Nav: "devices"}

	var err error
	if data.Templates, err = s.db.Templates(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data.Prefix = r.URL.Query().Get("prefix")
	data.Prefixes = s.prefixOptions(r)

	if key := r.URL.Query().Get("template"); key != "" {
		t, err := s.db.Template(r.Context(), key)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Selected, data.HasChoice = t, true
			data.Name = t.Name
		}
	}
	data.Elements = s.elementOptions("")

	s.render(w, "device_form", data)
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

	fail := func(msg string) {
		data := deviceFormData{
			Title: "Новое устройство", Nav: "devices",
			Error: msg, Name: name, Prefix: prefix,
			Prefixes: s.prefixOptions(r),
			Elements: s.elementOptions(""),
		}
		data.Templates, _ = s.db.Templates(r.Context())
		if t, err := s.db.Template(r.Context(), key); err == nil {
			data.Selected, data.HasChoice = t, true
		}
		s.render(w, "device_form", data)
	}

	tmpl, err := s.db.Template(r.Context(), key)
	if err != nil {
		fail(err.Error())
		return
	}

	// Роли без выбранного элемента просто пропускаются: не нужны показания —
	// не назначайте их.
	assign := make(map[string]devtmpl.Addr)
	for _, role := range tmpl.Roles {
		addr := trim(r.PostFormValue("role_" + role.Key))
		if addr == "" {
			continue
		}
		id, subID, err := parseAddr(addr)
		if err != nil {
			fail("роль «" + role.Title + "»: " + err.Error())
			return
		}
		assign[role.Key] = devtmpl.Addr{ID: id, SubID: subID}
	}

	_, err = s.db.ApplyTemplate(r.Context(),
		store.Device{Name: name, TopicPrefix: prefix}, tmpl, assign)
	if err != nil {
		fail(err.Error())
		return
	}

	s.reload(r)
	http.Redirect(w, r, "/devices", http.StatusSeeOther)
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
	http.Redirect(w, r, "/devices", http.StatusSeeOther)
}

// ---------------------------------------------------------------- Шаблоны

type templatesData struct {
	Title, Nav string
	Templates  []store.TemplateInfo
	Error      string
	Saved      string
}

func (s *server) pageTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.Templates(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "templates", templatesData{
		Title: "Шаблоны", Nav: "templates",
		Templates: list,
		Saved:     r.URL.Query().Get("saved"),
	})
}

// uploadTemplate принимает шаблон текстом или файлом.
func (s *server) uploadTemplate(w http.ResponseWriter, r *http.Request) {
	// Десять мегабайт с запасом: шаблон это несколько килобайт, но упереться
	// в предел на ровном месте неприятнее, чем подержать лишнее в памяти.
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "форма не разобрана", http.StatusBadRequest)
			return
		}
	}

	key := trim(r.PostFormValue("key"))
	body := []byte(r.PostFormValue("body"))

	// Файл важнее текстового поля: если приложили файл, значит им и хотели.
	if file, header, err := r.FormFile("file"); err == nil {
		defer func() { _ = file.Close() }()
		buf := make([]byte, header.Size)
		if _, err := file.Read(buf); err == nil || len(buf) > 0 {
			body = buf
		}
		if key == "" {
			key = strings.TrimSuffix(header.Filename, ".json")
		}
	}

	fail := func(msg string) {
		list, _ := s.db.Templates(r.Context())
		s.render(w, "templates", templatesData{
			Title: "Шаблоны", Nav: "templates",
			Templates: list, Error: msg,
		})
	}

	if len(body) == 0 {
		fail("шаблон пуст: вставьте JSON в поле или приложите файл")
		return
	}
	if key == "" {
		fail("не задан ключ шаблона — по нему он и опознаётся")
		return
	}

	if err := s.db.SaveTemplate(r.Context(), key, body); err != nil {
		fail(err.Error())
		return
	}
	http.Redirect(w, r, "/templates?saved="+key, http.StatusSeeOther)
}

func (s *server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteTemplate(r.Context(), r.PathValue("key")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/templates", http.StatusSeeOther)
}

// showTemplate отдаёт шаблон как JSON — чтобы скопировать и поправить.
func (s *server) showTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := s.db.Template(r.Context(), r.PathValue("key"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, t)
}
