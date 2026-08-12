// Package web — HTTP-интерфейс шлюза.
//
// Пока здесь проверка состояния, список увиденных топиков и заглушка на
// корне. Разделы «Элементы» и «Связки» появятся вместе с движком связок.
package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/staskuznec/mqtt2mimismart/internal/link"
	"github.com/staskuznec/mqtt2mimismart/internal/logic"
	"github.com/staskuznec/mqtt2mimismart/internal/mqtt"
	"github.com/staskuznec/mqtt2mimismart/internal/shs"
	"github.com/staskuznec/mqtt2mimismart/internal/store"
)

// healthTimeout — сколько ждём базу при проверке состояния. Проверка обязана
// отвечать быстро: по ней systemd и мониторинг судят, жив ли демон.
const healthTimeout = 2 * time.Second

// Status — источники состояния подключений. Поля пустые, пока настройки не
// заполнены и клиенты не созданы: веб обязан работать и до этого.
type Status struct {
	SHS      func() shs.Status
	MQTT     func() mqtt.Status
	Topics   func() []mqtt.TopicInfo
	Links    func() map[int64]link.Stats
	Elements func() []logic.Element

	// Reload заставляет движок перечитать связки. Вызывается после каждой
	// правки: иначе изменение вступало бы в силу только после перезапуска.
	Reload func(context.Context) error
}

// Handler собирает маршруты веб-интерфейса.
func Handler(log *slog.Logger, db *store.Store, version string, status Status) http.Handler {
	s := &server{log: log, db: db, version: version, status: status, started: time.Now()}

	mux := http.NewServeMux()

	// Страницы.
	mux.HandleFunc("GET /{$}", s.pageOverview)
	mux.HandleFunc("GET /settings", s.pageSettings)
	mux.HandleFunc("POST /settings", s.saveSettings)
	mux.HandleFunc("GET /topics", s.pageTopics)
	mux.HandleFunc("GET /links", s.pageLinks)
	mux.HandleFunc("GET /links/new", s.pageLinkForm)
	mux.HandleFunc("GET /links/{id}", s.pageLinkForm)
	mux.HandleFunc("POST /links/save", s.saveLink)
	mux.HandleFunc("POST /links/{id}/toggle", s.toggleLink)
	mux.HandleFunc("POST /links/{id}/delete", s.deleteLink)
	mux.HandleFunc("POST /links/preview", s.previewLink)
	mux.HandleFunc("GET /elements", s.pageElements)

	// Машинный интерфейс: на нём же потом стоит большая панель.
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/topics", s.topics)

	return s.logRequests(mux)
}

type server struct {
	log     *slog.Logger
	db      *store.Store
	version string
	status  Status
	started time.Time
}

// health отдаёт состояние демона в JSON.
type health struct {
	Status     string       `json:"status"`
	Version    string       `json:"version"`
	UptimeSec  int64        `json:"uptime_sec"`
	DBPath     string       `json:"db_path"`
	DBSchema   int          `json:"db_schema"`
	DBOK       bool         `json:"db_ok"`
	Configured bool         `json:"configured"`
	Links      *linksHealth `json:"links,omitempty"`
	SHS        *shsHealth   `json:"shs,omitempty"`
	MQTT       *mqttHealth  `json:"mqtt,omitempty"`
	Error      string       `json:"error,omitempty"`
}

type linksHealth struct {
	Delivered uint64 `json:"delivered"`
	Skipped   uint64 `json:"skipped"`
	Echoes    uint64 `json:"echoes"`
	Errors    uint64 `json:"errors"`
}

type shsHealth struct {
	Phase      string `json:"phase"`
	ClientID   uint16 `json:"client_id"`
	Events     uint64 `json:"events"`
	Sent       uint64 `json:"sent"`
	Connects   uint64 `json:"connects"`
	LogicBytes int    `json:"logic_bytes"`
	LastError  string `json:"last_error,omitempty"`
}

type mqttHealth struct {
	Connected   bool   `json:"connected"`
	ClientID    string `json:"client_id"`
	Received    uint64 `json:"received"`
	Published   uint64 `json:"published"`
	Connects    uint64 `json:"connects"`
	KnownTopics int    `json:"known_topics"`
	LastError   string `json:"last_error,omitempty"`
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()

	h := health{
		Status:    "ok",
		Version:   s.version,
		UptimeSec: int64(time.Since(s.started).Seconds()),
		DBPath:    s.db.Path(),
		DBOK:      true,
	}

	if err := s.db.Ping(ctx); err != nil {
		h.Status, h.DBOK, h.Error = "degraded", false, err.Error()
		s.writeJSON(w, http.StatusServiceUnavailable, h)
		return
	}
	if schema, err := s.db.SchemaVersion(ctx); err == nil {
		h.DBSchema = schema
	}
	if cfg, err := s.db.Config(ctx); err == nil {
		h.Configured = cfg.Ready()
	}

	if s.status.SHS != nil {
		st := s.status.SHS()
		h.SHS = &shsHealth{
			Phase:      string(st.Phase),
			ClientID:   st.ClientID,
			Events:     st.Events,
			Sent:       st.Sent,
			Connects:   st.Connects,
			LogicBytes: st.LogicBytes,
			LastError:  st.LastError,
		}
		if st.Phase != shs.PhaseConnected && st.Phase != shs.PhaseSyncing {
			h.Status = "degraded"
		}
	}
	if s.status.MQTT != nil {
		st := s.status.MQTT()
		h.MQTT = &mqttHealth{
			Connected:   st.Connected,
			ClientID:    st.ClientID,
			Received:    st.Received,
			Published:   st.Published,
			Connects:    st.Connects,
			KnownTopics: st.KnownTopics,
			LastError:   st.LastError,
		}
		if !st.Connected {
			h.Status = "degraded"
		}
	}

	if s.status.Links != nil {
		var total linksHealth
		for _, st := range s.status.Links() {
			total.Delivered += st.Delivered
			total.Skipped += st.Skipped
			total.Echoes += st.Echoes
			total.Errors += st.Errors
		}
		h.Links = &total
	}

	// Отсутствие связи — не повод отвечать ошибкой: демон жив, и веб обязан
	// открываться, чтобы связь было где починить.
	s.writeJSON(w, http.StatusOK, h)
}

// topicView — топик в том виде, в каком его показывает веб.
type topicView struct {
	Topic     string `json:"topic"`
	Payload   string `json:"payload"`
	Truncated bool   `json:"truncated,omitempty"`
	Kind      string `json:"kind"`
	Retained  bool   `json:"retained,omitempty"`
	Count     uint64 `json:"count"`
	LastAt    string `json:"last_at"`
}

// topics отдаёт содержимое снифера — то, что реально ходит по шине.
func (s *server) topics(w http.ResponseWriter, _ *http.Request) {
	if s.status.Topics == nil {
		s.writeJSON(w, http.StatusOK, []topicView{})
		return
	}

	seen := s.status.Topics()
	out := make([]topicView, 0, len(seen))
	for _, t := range seen {
		out = append(out, topicView{
			Topic:     t.Topic,
			Payload:   string(t.LastPayload),
			Truncated: t.Truncated,
			Kind:      t.Kind,
			Retained:  t.Retained,
			Count:     t.Count,
			LastAt:    t.LastAt.Format(time.RFC3339),
		})
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Заголовок уже ушёл, менять код поздно — остаётся записать в журнал.
		s.log.Error("отправка ответа", "err", err)
	}
}

// logRequests пишет каждый запрос в журнал на уровне debug.
func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		s.log.Debug("запрос",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"ms", time.Since(started).Milliseconds())
	})
}

// statusRecorder запоминает код ответа: сам http.ResponseWriter его не отдаёт.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
