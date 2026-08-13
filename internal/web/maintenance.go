package web

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/staskuznec/mqtt2mimismart/internal/logging"
)

// ---------------------------------------------------------------- Журнал

type logData struct {
	Title, Nav string
	Entries    []logRow
	Level      string
}

type logRow struct {
	Time   string
	Level  string
	Class  string // ok | warn | bad — для цвета
	Text   string
	Detail string
}

func (s *server) pageLog(w http.ResponseWriter, r *http.Request) {
	level := r.URL.Query().Get("level")
	minLevel := slog.LevelInfo
	switch level {
	case "debug":
		minLevel = slog.LevelDebug
	case "warn":
		minLevel = slog.LevelWarn
	case "error":
		minLevel = slog.LevelError
	default:
		level = "info"
	}

	data := logData{Title: "Журнал", Nav: "log", Level: level}
	if s.status.Log == nil {
		s.render(w, "log", data)
		return
	}

	for _, e := range s.status.Log(300, minLevel) {
		row := logRow{
			Time:  e.At.Format("15:04:05"),
			Level: strings.ToLower(e.Level.String()),
			Text:  e.Message,
		}
		switch {
		case e.Level >= slog.LevelError:
			row.Class = "bad"
		case e.Level >= slog.LevelWarn:
			row.Class = "warn"
		}
		row.Detail = formatAttrs(e)
		data.Entries = append(data.Entries, row)
	}
	s.render(w, "log", data)
}

// formatAttrs собирает подробности записи в одну строку.
func formatAttrs(e logging.Entry) string {
	if len(e.Attrs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(e.Attrs))
	for k, v := range e.Attrs {
		parts = append(parts, k+"="+v)
	}
	// Порядок карты случайный, а строка должна читаться одинаково.
	sortStrings(parts)
	return strings.Join(parts, "  ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ---------------------------------------------------------------- Обновление

// applyUpdate скачивает новую версию и заменяет ею работающий бинарник.
//
// Шлюз при этом завершается, а поднимает его обратно systemd. Иначе пришлось
// бы идти в консоль ради того, что уже описано в интерфейсе.
func (s *server) applyUpdate(w http.ResponseWriter, r *http.Request) {
	if s.status.Upgrade == nil {
		http.Error(w, "обновление недоступно", http.StatusNotImplemented)
		return
	}

	if err := s.status.Upgrade(r.Context()); err != nil {
		s.log.Error("обновление не удалось", "err", err)
		s.render(w, "updated", updateResult{Error: err.Error()})
		return
	}

	// Ответ отдаём до выхода: иначе браузер получит оборванное соединение и
	// покажет ошибку там, где всё прошло успешно.
	s.render(w, "updated", updateResult{OK: true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	go func() {
		// Небольшая пауза, чтобы ответ успел уйти в сеть.
		time.Sleep(time.Second)
		s.log.Info("перезапуск после обновления")
		if s.status.Restart != nil {
			s.status.Restart()
		}
	}()
}

type updateResult struct {
	OK    bool
	Error string
}
