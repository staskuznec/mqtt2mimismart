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
func New(w io.Writer, level, format string) (*slog.Logger, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}

	opts := &slog.HandlerOptions{Level: lvl}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", FormatText:
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case FormatJSON:
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("формат журнала %q: допустимы %s и %s", format, FormatText, FormatJSON)
	}
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
