// Package web — HTTP-интерфейс шлюза.
//
// Пока здесь только проверка состояния и заглушка на корне. Разделы «Обзор»,
// «Топики», «Элементы» и «Связки» появятся, когда заработают клиенты MQTT и
// SHS: показывать в них до этого нечего.
package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/staskuznec/mqtt2mimismart/internal/store"
)

// healthTimeout — сколько ждём базу при проверке состояния. Проверка обязана
// отвечать быстро: по ней systemd и мониторинг судят, жив ли демон.
const healthTimeout = 2 * time.Second

// Handler собирает маршруты веб-интерфейса.
func Handler(log *slog.Logger, db *store.Store, version string) http.Handler {
	s := &server{log: log, db: db, version: version, started: time.Now()}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /{$}", s.index)

	return s.logRequests(mux)
}

type server struct {
	log     *slog.Logger
	db      *store.Store
	version string
	started time.Time
}

// health отдаёт состояние демона в JSON.
type health struct {
	Status     string `json:"status"`
	Version    string `json:"version"`
	UptimeSec  int64  `json:"uptime_sec"`
	DBPath     string `json:"db_path"`
	DBSchema   int    `json:"db_schema"`
	DBOK       bool   `json:"db_ok"`
	Configured bool   `json:"configured"`
	Error      string `json:"error,omitempty"`
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

	s.writeJSON(w, http.StatusOK, h)
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("mqtt2mimismart " + s.version + "\n\nВеб-интерфейс в разработке.\nСостояние: /healthz\n"))
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
