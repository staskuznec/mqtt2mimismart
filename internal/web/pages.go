package web

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/staskuznec/mqtt2mimismart/internal/link"
	"github.com/staskuznec/mqtt2mimismart/internal/mqtt"
	"github.com/staskuznec/mqtt2mimismart/internal/shs"
	"github.com/staskuznec/mqtt2mimismart/internal/store"
	"github.com/staskuznec/mqtt2mimismart/internal/update"
)

// Страницы вшиты в бинарник: на сервер кладётся один файл, и забыть скопировать
// каталог с шаблонами невозможно.
//
//go:embed templates/*.html
var templateFS embed.FS

// pages — разобранные шаблоны. Каждая страница собирается вместе с общим
// оформлением: html/template не умеет переопределять блок в рамках одного
// набора, поэтому наборов столько же, сколько страниц.
var pages = map[string]*template.Template{
	"overview":    mustParse("overview.html"),
	"settings":    mustParse("settings.html"),
	"topics":      mustParse("topics.html"),
	"links":       mustParse("links.html"),
	"link_form":   mustParse("link_form.html"),
	"elements":    mustParse("elements.html"),
	"devices":     mustParse("devices.html"),
	"device_form": mustParse("device_form.html"),
	"templates":   mustParse("templates.html"),

	// Проба возвращается кусочком страницы, поэтому общее оформление ей
	// не нужно — она подставляется в уже открытую форму.
	"preview": template.Must(template.ParseFS(templateFS, "templates/preview.html")),
}

func mustParse(name string) *template.Template {
	return template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/"+name))
}

// render отдаёт страницу.
func (s *server) render(w http.ResponseWriter, page string, data any) {
	tmpl, ok := pages[page]
	if !ok {
		http.Error(w, "нет такой страницы", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		// Часть страницы уже ушла в сокет, менять код поздно.
		s.log.Error("отрисовка страницы", "page", page, "err", err)
	}
}

// ---------------------------------------------------------------- Обзор

type overviewData struct {
	Title, Nav string
	Version    string
	Schema     int
	DBPath     string
	Configured bool
	MQTT       *mqtt.Status
	SHS        *shs.Status
	SHSPhase   string
	SHSDot     string
	Links      linksSummary
	Update     *update.Info
}

type linksSummary struct {
	Total, Enabled                     int
	Delivered, Skipped, Echoes, Errors uint64
}

func (s *server) pageOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := overviewData{
		Title: "Обзор", Nav: "overview",
		Version: s.version, DBPath: s.db.Path(),
	}
	if schema, err := s.db.SchemaVersion(ctx); err == nil {
		data.Schema = schema
	}
	if cfg, err := s.db.Config(ctx); err == nil {
		data.Configured = cfg.Ready()
	}

	if s.status.MQTT != nil {
		st := s.status.MQTT()
		data.MQTT = &st
	}
	if s.status.SHS != nil {
		st := s.status.SHS()
		data.SHS = &st
		data.SHSPhase, data.SHSDot = phaseView(st.Phase)
	}

	if links, err := s.db.Links(ctx); err == nil {
		data.Links.Total = len(links)
		for _, l := range links {
			if l.Enabled {
				data.Links.Enabled++
			}
		}
	}
	if s.status.Links != nil {
		for _, st := range s.status.Links() {
			data.Links.Delivered += st.Delivered
			data.Links.Skipped += st.Skipped
			data.Links.Echoes += st.Echoes
			data.Links.Errors += st.Errors
		}
	}

	if s.status.Update != nil {
		info := s.status.Update()
		data.Update = &info
	}

	s.render(w, "overview", data)
}

// phaseView переводит состояние соединения в текст и цвет точки.
func phaseView(p shs.Phase) (text, dot string) {
	switch p {
	case shs.PhaseConnected:
		return "на связи", "ok"
	case shs.PhaseSyncing:
		return "синхронизация состояний", "warn"
	case shs.PhaseConnecting:
		return "подключение", "warn"
	default:
		return "связи нет", "bad"
	}
}

// ---------------------------------------------------------------- Настройки

type settingsData struct {
	Title, Nav string
	Config     store.Config
	Saved      bool
	Error      string
}

func (s *server) pageSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.db.Config(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "settings", settingsData{
		Title: "Настройки", Nav: "settings",
		Config: cfg,
		Saved:  r.URL.Query().Get("saved") == "1",
	})
}

func (s *server) saveSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "форма не разобрана", http.StatusBadRequest)
		return
	}

	cfg := store.Config{
		MQTTAddr:     trim(r.PostFormValue("mqtt_addr")),
		MQTTUser:     trim(r.PostFormValue("mqtt_user")),
		MQTTPassword: r.PostFormValue("mqtt_password"),
		MQTTClientID: trim(r.PostFormValue("mqtt_client_id")),
		SHSAddr:      trim(r.PostFormValue("shs_addr")),
		SHSKey:       r.PostFormValue("shs_key"),
		SHSMac:       trim(r.PostFormValue("shs_mac")),
	}

	if err := s.db.SaveConfig(r.Context(), cfg); err != nil {
		// Форму отдаём обратно с введёнными значениями: заставлять набирать
		// ключ заново из-за опечатки в адресе — издевательство.
		s.render(w, "settings", settingsData{
			Title: "Настройки", Nav: "settings",
			Config: cfg, Error: err.Error(),
		})
		return
	}

	s.log.Info("настройки сохранены", "mqtt", cfg.MQTTAddr, "shs", cfg.SHSAddr)
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

// ---------------------------------------------------------------- Топики

type topicsData struct {
	Title, Nav string
	Groups     []topicGroup
	Total      int
	Overflow   uint64
}

// topicGroup — топики одного устройства, собранные по мастер-топику.
type topicGroup struct {
	Prefix string
	Topics []topicRow
}

type topicRow struct {
	Topic     string
	Short     string // топик без мастер-части: она и так в заголовке группы
	Payload   string
	Truncated bool
	Kind      string
	Retained  bool
	Count     uint64
	Ago       string
}

func (s *server) pageTopics(w http.ResponseWriter, _ *http.Request) {
	data := topicsData{Title: "Топики", Nav: "topics"}
	if s.status.Topics == nil {
		s.render(w, "topics", data)
		return
	}

	// Группируем по первым двум уровням: у Shelly это "shellies/<id>", и такая
	// же двухуровневая схема у большинства прошивок. Одно устройство — одна
	// таблица, иначе на объекте с десятком реле ничего не найти.
	order := make([]string, 0, 8)
	groups := make(map[string][]topicRow)

	for _, t := range s.status.Topics() {
		prefix := topicPrefix(t.Topic)
		if _, ok := groups[prefix]; !ok {
			order = append(order, prefix)
		}
		groups[prefix] = append(groups[prefix], topicRow{
			Topic:     t.Topic,
			Short:     strings.TrimPrefix(strings.TrimPrefix(t.Topic, prefix), "/"),
			Payload:   string(t.LastPayload),
			Truncated: t.Truncated,
			Kind:      t.Kind,
			Retained:  t.Retained,
			Count:     t.Count,
			Ago:       ago(t.LastAt),
		})
		data.Total++
	}

	for _, prefix := range order {
		data.Groups = append(data.Groups, topicGroup{Prefix: prefix, Topics: groups[prefix]})
	}
	s.render(w, "topics", data)
}

// topicPrefix выделяет мастер-топик устройства — первые два уровня.
func topicPrefix(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		return topic
	}
	return parts[0] + "/" + parts[1]
}

// ---------------------------------------------------------------- Связки

type linksData struct {
	Title, Nav string
	Groups     []linkGroup
	Total      int
}

// linkGroup — связки одного устройства.
//
// Устройства различаются мастер-топиком: "shellies/shellyswitch25-4022D8956527"
// у одного, другой префикс у другого. Валить их в общий список — значит искать
// нужную строку глазами среди сотни чужих.
type linkGroup struct {
	Device string // имя устройства
	Prefix string // мастер-топик
	Links  []linkRow
}

type linkRow struct {
	ID           int64
	PairID       int64
	Enabled      bool
	Arrow        string
	Topic        string
	CommandTopic string // у пары — топик команды, второй строкой
	Addr         string
	Name         string
	Form         string
	LastValue    string
	Errors       uint64
	LastError    string
	Paired       bool
}

func (s *server) pageLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.db.Links(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	devices, err := s.db.Devices(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byID := make(map[int64]store.Device, len(devices))
	for _, d := range devices {
		byID[d.ID] = d
	}

	var stats map[int64]link.Stats
	if s.status.Links != nil {
		stats = s.status.Links()
	}

	// Двусторонняя привязка показывается одной строкой: две — это внутреннее
	// устройство, а на экране от них только рябит. Правится и переключается
	// она тоже как одна.
	rows := make(map[int64][]linkRow)  // устройство → строки
	byPair := make(map[int64][2]int64) // метка пары → устройство и номер строки

	for _, l := range links {
		st := stats[l.ID]

		if l.PairID != 0 {
			if pos, ok := byPair[l.PairID]; ok {
				group := rows[pos[0]]
				row := &group[pos[1]]
				row.Arrow = "шина ⇄ дом"
				if l.Direction == link.Out {
					row.CommandTopic = l.Topic
				} else {
					// Входящая сторона задаёт основной топик, форму и адрес
					// для правки: форма связки открывается именно с неё.
					row.CommandTopic, row.Topic = row.Topic, l.Topic
					row.Form, row.ID = l.Encode, l.ID
				}
				row.Errors += st.Errors
				if st.LastError != "" {
					row.LastError = st.LastError
				}
				if row.LastValue == "" {
					row.LastValue = st.LastValue
				}
				continue
			}
			byPair[l.PairID] = [2]int64{l.DeviceID, int64(len(rows[l.DeviceID]))}
		}

		row := linkRow{
			ID: l.ID, PairID: l.PairID, Enabled: l.Enabled, Topic: l.Topic,
			Addr: l.Addr(), Name: l.Name, Paired: l.PairID != 0,
			LastValue: st.LastValue, Errors: st.Errors, LastError: st.LastError,
		}
		if l.Direction == link.Out {
			row.Arrow, row.Form = "дом → шина", l.Decode
		} else {
			row.Arrow, row.Form = "шина → дом", l.Encode
		}
		rows[l.DeviceID] = append(rows[l.DeviceID], row)
	}

	data := linksData{Title: "Связки", Nav: "links", Total: len(links)}

	// Устройства по порядку, затем связки, заведённые вручную.
	for _, d := range devices {
		if group, ok := rows[d.ID]; ok {
			data.Groups = append(data.Groups, linkGroup{
				Device: d.Name, Prefix: d.TopicPrefix, Links: group,
			})
		}
	}
	if loose, ok := rows[0]; ok {
		data.Groups = append(data.Groups, linkGroup{Device: "Вне устройств", Links: loose})
	}

	s.render(w, "links", data)
}

// ---------------------------------------------------------------- Общее

// ago переводит время в «сколько прошло»: точная отметка в таблице, где
// значения меняются каждую секунду, читается хуже, чем «3 с назад».
func ago(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d с назад", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d мин назад", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d ч назад", int(d.Hours()))
	default:
		return t.Format("02.01 15:04")
	}
}

// trim убирает пробелы по краям: в поля адресов их заносят копированием
// постоянно, а "127.0.0.1:1883 " не подключится.
func trim(s string) string { return strings.TrimSpace(s) }
