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
	"overview":  mustParse("overview.html"),
	"settings":  mustParse("settings.html"),
	"topics":    mustParse("topics.html"),
	"links":     mustParse("links.html"),
	"link_form": mustParse("link_form.html"),
	"elements":  mustParse("elements.html"),

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
		LogicPath:    trim(r.PostFormValue("logic_path")),
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
	Topics     []topicRow
	Overflow   uint64
}

type topicRow struct {
	Topic     string
	Payload   string
	Truncated bool
	Kind      string
	Retained  bool
	Count     uint64
	Ago       string
}

func (s *server) pageTopics(w http.ResponseWriter, _ *http.Request) {
	data := topicsData{Title: "Топики", Nav: "topics"}
	if s.status.Topics != nil {
		for _, t := range s.status.Topics() {
			data.Topics = append(data.Topics, topicRow{
				Topic:     t.Topic,
				Payload:   string(t.LastPayload),
				Truncated: t.Truncated,
				Kind:      t.Kind,
				Retained:  t.Retained,
				Count:     t.Count,
				Ago:       ago(t.LastAt),
			})
		}
	}
	s.render(w, "topics", data)
}

// ---------------------------------------------------------------- Связки

type linksData struct {
	Title, Nav string
	Links      []linkRow
}

type linkRow struct {
	ID        int64
	Enabled   bool
	Arrow     string
	Topic     string
	Addr      string
	Name      string
	Form      string
	LastValue string
	Errors    uint64
	LastError string
}

func (s *server) pageLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.db.Links(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var stats map[int64]link.Stats
	if s.status.Links != nil {
		stats = s.status.Links()
	}

	data := linksData{Title: "Связки", Nav: "links"}
	for _, l := range links {
		row := linkRow{
			ID: l.ID, Enabled: l.Enabled, Topic: l.Topic,
			Addr: l.Addr(), Name: l.Name,
		}
		if l.Direction == link.Out {
			row.Arrow, row.Form = "дом → шина", l.Decode
		} else {
			row.Arrow, row.Form = "шина → дом", l.Encode
		}
		if st, ok := stats[l.ID]; ok {
			row.LastValue, row.Errors, row.LastError = st.LastValue, st.Errors, st.LastError
		}
		data.Links = append(data.Links, row)
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
