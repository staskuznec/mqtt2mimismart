// Демон-шлюз между MQTT и умным домом MimiSmart.
//
// Держит постоянные соединения с брокером и с сервером умного дома и переводит
// сообщения из одного в другое по правилам, которые задаются в веб-интерфейсе.
// Конфигов на диске нет: рядом с бинарником лежит только файл базы, а всё
// остальное — брокер, сервер умного дома, ключ, связки — вводится в вебе.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/staskuznec/mqtt2mimismart/internal/app"
	"github.com/staskuznec/mqtt2mimismart/internal/devtmpl"
	"github.com/staskuznec/mqtt2mimismart/internal/logging"
	"github.com/staskuznec/mqtt2mimismart/internal/store"
)

// version подставляется при сборке через -ldflags.
var version = "dev"

// DefaultDBName — имя файла базы рядом с бинарником, если путь не задан.
const DefaultDBName = "gateway.db"

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
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
		basePath  = flag.String("base-path", "", "подкаталог, если шлюз стоит за веб-сервером: /mqtt")
		profiles  = flag.String("profiles", "", "каталог с профилями устройств (по умолчанию profiles рядом с базой)")
		logLevel  = flag.String("log-level", "info", "уровень журнала: debug, info, warn, error")
		logFormat = flag.String("log-format", logging.FormatText, "формат журнала: text или json")
		showVer   = flag.Bool("version", false, "показать версию и выйти")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return nil
	}

	log, ring, err := logging.New(os.Stderr, *logLevel, *logFormat)
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

	// Профили устройств лежат файлами: их видно, их можно открыть редактором,
	// положить туда свой или скопировать с другого объекта.
	profilesDir := *profiles
	if profilesDir == "" {
		profilesDir = filepath.Join(filepath.Dir(db.Path()), "profiles")
	}
	dir, err := devtmpl.Open(profilesDir)
	if err != nil {
		return err
	}
	db.SetTemplates(dir)

	schema, err := db.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	log.Info("шлюз запускается",
		"version", version,
		"db", db.Path(),
		"schema", schema,
		"profiles", dir.Path(),
		"addr", *addr)

	a, err := app.New(log, ring, db, version, *addr, *basePath)
	if err != nil {
		return err
	}

	err = a.Run(ctx)
	log.Info("остановлен")
	return err
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
