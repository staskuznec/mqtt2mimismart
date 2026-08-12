// Package logger настраивает zerolog по конфигу.
package logger

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/staskuznec/mqtt2mimismart/internal/config"
)

// Init инициализирует глобальный логгер zerolog.
// Логи идут в stderr, чтобы не мешать умному дому читать stdout.
func Init(cfg config.Logging) {
	if strings.ToLower(cfg.Format) == "json" {
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	} else {
		log.Logger = log.Output(consoleWriter())
	}

	level := strings.ToLower(strings.TrimSpace(cfg.Level))
	if level == "" {
		level = "error"
	}
	switch level {
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "info":
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case "warn", "warning":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
		log.Warn().Str("level", cfg.Level).Msg("Unknown log level, defaulting to 'error'")
	}
}

// consoleWriter — текстовый вывод в стиле slog (key=value, без цветов).
func consoleWriter() zerolog.ConsoleWriter {
	return zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: time.RFC3339,
		NoColor:    true,
		PartsOrder: []string{
			zerolog.TimestampFieldName,
			zerolog.LevelFieldName,
			zerolog.MessageFieldName,
		},
		FormatTimestamp:  func(i any) string { return fmt.Sprintf("time=%s", i) },
		FormatLevel:      func(i any) string { return fmt.Sprintf("level=%s", strings.ToUpper(fmt.Sprintf("%s", i))) },
		FormatMessage:    func(i any) string { return fmt.Sprintf("msg=%q", i) },
		FormatFieldName:  func(i any) string { return fmt.Sprintf("%s=", i) },
		FormatFieldValue: formatFieldValue,
	}
}

func formatFieldValue(i any) string {
	if s, ok := i.(string); ok && strings.Contains(s, " ") {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprintf("%v", i)
}
