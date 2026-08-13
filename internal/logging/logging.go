// Package logging настраивает журнал шлюза.
//
// Взят стандартный log/slog, а не zerolog, которым пользуется старый бинарник
// реле. Причина простая: библиотека клиента SHS принимает именно *slog.Logger,
// и так её вывод попадает в общий журнал без переходников.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Формат вывода.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// New собирает журнал. Уровень: debug, info, warn, error.
//
// Кроме обычного вывода записи складываются в кольцо: шлюз работает службой, и
// последние сообщения должны быть видны прямо в вебе — иначе понять, почему не
// подключается брокер, можно только через journalctl.
func New(w io.Writer, level, format string) (*slog.Logger, *Ring, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, nil, err
	}

	opts := &slog.HandlerOptions{Level: lvl}

	var base slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", FormatText:
		base = slog.NewTextHandler(w, opts)
	case FormatJSON:
		base = slog.NewJSONHandler(w, opts)
	default:
		return nil, nil, fmt.Errorf("формат журнала %q: допустимы %s и %s", format, FormatText, FormatJSON)
	}

	ring := NewRing()
	return slog.New(&recorder{next: base, ring: ring}), ring, nil
}

func parseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("уровень журнала %q: допустимы debug, info, warn, error", level)
	}
}
