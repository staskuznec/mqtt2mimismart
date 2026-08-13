// Package store — хранилище шлюза поверх SQLite.
//
// Драйвер взят чистый на Go (modernc.org/sqlite): он не требует cgo, поэтому
// сборка под ARMv7 остаётся такой же однострочной, как и под хост.
//
// Файл базы создаётся с правами 0600 — в нём лежат ключ AES от сервера умного
// дома и пароль к брокеру.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/staskuznec/mqtt2mimismart/internal/devtmpl"
)

// FileMode — права на файл базы. Внутри секреты, читать её посторонним незачем.
const FileMode os.FileMode = 0o600

// Store — открытая база со всеми накатанными миграциями.
type Store struct {
	db        *sql.DB
	path      string
	templates *devtmpl.Dir // шаблоны лежат файлами, а не в базе
}

// Open открывает базу по пути path, создавая её при необходимости, и приводит
// схему к последней версии. Каталог под файл создаётся, если его нет.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: путь к базе %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("store: каталог для базы: %w", err)
	}

	// busy_timeout задаётся прямо в DSN, чтобы он действовал уже на первых
	// запросах — включая те, которыми накатываются миграции.
	dsn := "file:" + abs + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: открытие базы %s: %w", abs, err)
	}

	s := &Store{db: db, path: abs}
	if err := s.prepare(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(abs, FileMode); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: права на %s: %w", abs, err)
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// prepare выставляет режимы, от которых зависит поведение под нагрузкой.
func (s *Store) prepare(ctx context.Context) error {
	pragmas := []string{
		// Журнал с упреждающей записью: веб читает, движок пишет, и они друг
		// друга не блокируют. Без него любая запись останавливает читателей.
		"PRAGMA journal_mode = WAL",
		// С WAL этого достаточно: транзакция не теряется при падении процесса,
		// потерять её может только внезапное отключение питания. Взамен на
		// флеш-картах, где обычно живут такие серверы, запись быстрее в разы.
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
	}
	for _, p := range pragmas {
		if _, err := s.db.ExecContext(ctx, p); err != nil {
			return fmt.Errorf("store: %s: %w", p, err)
		}
	}
	return nil
}

// Close закрывает базу. Повторный вызов безопасен.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// DB отдаёт соединение для запросов из соседних пакетов.
func (s *Store) DB() *sql.DB { return s.db }

// Path возвращает абсолютный путь к файлу базы.
func (s *Store) Path() string { return s.path }

// Ping проверяет, что база жива. Используется проверкой состояния демона.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("store: база не открыта")
	}
	return s.db.PingContext(ctx)
}
