package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Миграции лежат файлами рядом и вшиваются в бинарник: отдельный каталог со
// схемой рядом с исполняемым файлом на сервере — лишняя вещь, которую можно
// забыть скопировать.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migration — один шаг схемы. Имя файла задаёт и версию, и описание:
// "0001_init.sql" → версия 1, имя "init".
type migration struct {
	version int
	name    string
	sql     string
}

// migrate приводит схему к последней версии, применяя недостающие шаги по
// порядку. Каждый шаг идёт одной транзакцией вместе с отметкой о применении:
// если он упал на середине, база останется на предыдущей версии целиком, а не
// в состоянии «половина накатилась».
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("store: таблица миграций: %w", err)
	}

	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return err
	}

	all, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range all {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if err := s.apply(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// apply накатывает один шаг.
func (s *Store) apply(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: миграция %04d_%s: начало транзакции: %w", m.version, m.name, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("store: миграция %04d_%s: %w", m.version, m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, time.Now().Unix()); err != nil {
		return fmt.Errorf("store: миграция %04d_%s: отметка о применении: %w", m.version, m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: миграция %04d_%s: фиксация: %w", m.version, m.name, err)
	}
	return nil
}

// appliedVersions читает уже применённые версии.
func (s *Store) appliedVersions(ctx context.Context) (map[int]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: чтение применённых миграций: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]struct{})
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("store: чтение применённых миграций: %w", err)
		}
		applied[v] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: чтение применённых миграций: %w", err)
	}
	return applied, nil
}

// SchemaVersion возвращает версию схемы: номер последней применённой миграции,
// либо 0 на пустой базе.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("store: версия схемы: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// loadMigrations читает вшитые файлы и сортирует их по версии.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: чтение миграций: %w", err)
	}

	out := make([]migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(e.Name())
		if err != nil {
			return nil, err
		}
		// Две миграции с одним номером — это опечатка при копировании файла.
		// Молча применить одну из них хуже, чем отказаться стартовать.
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("store: версия миграции %d занята дважды: %s и %s", version, prev, e.Name())
		}
		seen[version] = e.Name()

		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: чтение %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: name, sql: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// parseMigrationName разбирает "0001_init.sql" в версию и имя.
func parseMigrationName(file string) (int, string, error) {
	base := strings.TrimSuffix(file, ".sql")
	numStr, name, found := strings.Cut(base, "_")
	if !found {
		return 0, "", fmt.Errorf("store: имя миграции %q: ожидался формат NNNN_имя.sql", file)
	}
	version, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, "", fmt.Errorf("store: имя миграции %q: номер не число", file)
	}
	if version <= 0 {
		return 0, "", fmt.Errorf("store: имя миграции %q: номер должен быть больше нуля", file)
	}
	return version, name, nil
}
