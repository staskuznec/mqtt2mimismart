// Демон-шлюз между MQTT и умным домом MimiSmart.
//
// Держит постоянные соединения с брокером и с сервером умного дома и переводит
// сообщения из одного в другое по правилам, которые задаются в веб-интерфейсе.
// Конфигов на диске нет: рядом с бинарником лежит только файл базы.
//
// Пока это скелет — база, журнал, веб-сервер и корректное завершение. Клиенты
// MQTT и SHS подключаются следующими этапами.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/staskuznec/mqtt2mimismart/internal/logging"
	"github.com/staskuznec/mqtt2mimismart/internal/store"
	"github.com/staskuznec/mqtt2mimismart/internal/web"
)

// version подставляется при сборке через -ldflags.
var version = "dev"

// DefaultDBName — имя файла базы рядом с бинарником, если путь не задан.
const DefaultDBName = "gateway.db"

// Время на то, чтобы доработать начатые запросы при остановке. Больше ждать
// незачем: systemd всё равно прибьёт процесс по своему таймауту.
const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		// Журнал к этому моменту может быть ещё не настроен, поэтому пишем
		// прямо в stderr — иначе причина падения просто потеряется.
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr      = flag.String("addr", "127.0.0.1:8080", "адрес веб-интерфейса, host:port")
		dbPath    = flag.String("db", "", "путь к файлу базы (по умолчанию "+DefaultDBName+" рядом с бинарником)")
		logLevel  = flag.String("log-level", "info", "уровень журнала: debug, info, warn, error")
		logFormat = flag.String("log-format", logging.FormatText, "формат журнала: text или json")
		showVer   = flag.Bool("version", false, "показать версию и выйти")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return nil
	}

	log, err := logging.New(os.Stderr, *logLevel, *logFormat)
	if err != nil {
		return err
	}

	path, err := resolveDBPath(*dbPath)
	if err != nil {
		return err
	}

	// Контекст живёт до Ctrl+C или SIGTERM от systemd.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Error("закрытие базы", "err", err)
		}
	}()

	schema, err := db.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	cfg, err := db.Config(ctx)
	if err != nil {
		return err
	}

	log.Info("шлюз запускается",
		"version", version,
		"db", db.Path(),
		"schema", schema,
		"addr", *addr)
	if !cfg.Ready() {
		log.Warn("настройки не заполнены, откройте веб-интерфейс и завершите первый запуск",
			"url", "http://"+*addr)
	}

	return serve(ctx, log, db, *addr)
}

// serve поднимает веб-сервер и держит его до отмены контекста.
func serve(ctx context.Context, log *slog.Logger, db *store.Store, addr string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: web.Handler(log, db, version),

		// Голые таймауты обязательны: без них одно зависшее соединение
		// занимает горутину до конца жизни процесса.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("веб-интерфейс слушает", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("веб-сервер: %w", err)
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("получен сигнал, останавливаемся")
	}

	// Свой контекст: тот, что пришёл, уже отменён сигналом, и остановка по
	// нему завершилась бы мгновенно, оборвав запросы на середине.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("остановка веб-сервера: %w", err)
	}
	log.Info("остановлен")
	return nil
}

// resolveDBPath определяет, где лежит база.
//
// Без флага — рядом с бинарником, как это заведено в проекте: файлы кладутся в
// один каталог на сервере и переносятся целиком. В systemd-юните путь задаётся
// явно, чтобы база не оказалась в каталоге с исполняемым файлом.
func resolveDBPath(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("определение пути к бинарнику: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), DefaultDBName), nil
}
