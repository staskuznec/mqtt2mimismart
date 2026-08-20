package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Device — группа связок с общим префиксом топика.
type Device struct {
	ID          int64
	Name        string
	TopicPrefix string // "shellies/shelly25-A1B2C3"
	Model       string // из announce: "SHSW-25"
	Template    string // имя применённого шаблона
	Online      bool
	LastSeen    time.Time
}

const deviceColumns = `id, name, topic_prefix, model, template, online, last_seen`

// Devices читает все устройства.
func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceColumns+` FROM devices ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("store: чтение устройств: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Device читает одно устройство.
func (s *Store) Device(ctx context.Context, id int64) (Device, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE id = ?`, id)
	d, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, fmt.Errorf("%w: устройство %d", ErrNotFound, id)
	}
	return d, err
}

// DeviceByPrefix ищет устройство по префиксу топика. Нужен обнаружению:
// по announce мы знаем префикс и должны понять, новое это устройство или нет.
func (s *Store) DeviceByPrefix(ctx context.Context, prefix string) (Device, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+deviceColumns+` FROM devices WHERE topic_prefix = ?`, prefix)
	d, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, fmt.Errorf("%w: устройство с префиксом %q", ErrNotFound, prefix)
	}
	return d, err
}

// CreateDevice заводит устройство.
func (s *Store) CreateDevice(ctx context.Context, d Device) (int64, error) {
	if strings.TrimSpace(d.Name) == "" {
		return 0, fmt.Errorf("store: у устройства пустое имя")
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO devices (name, topic_prefix, model, template, online, last_seen, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.Name, d.TopicPrefix, d.Model, d.Template, d.Online, nullableTime(d.LastSeen),
		time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("store: сохранение устройства: %w", err)
	}
	return res.LastInsertId()
}

// UpdateDevice перезаписывает устройство.
func (s *Store) UpdateDevice(ctx context.Context, d Device) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE devices SET name = ?, topic_prefix = ?, model = ?, template = ?,
			online = ?, last_seen = ?
		WHERE id = ?`,
		d.Name, d.TopicPrefix, d.Model, d.Template, d.Online, nullableTime(d.LastSeen), d.ID)
	if err != nil {
		return fmt.Errorf("store: обновление устройства: %w", err)
	}
	return checkAffected(res, "устройство", d.ID)
}

// RetargetDevices переводит устройства с одного профиля на другой.
//
// Нужно, когда правка поставляемого профиля уезжает в копию: устройство
// настраивали по правленому профилю, и открыть его надо на той же копии, а не
// на вернувшемся эталоне. Связки при этом не трогаются — они уже заведены и
// работают.
func (s *Store) RetargetDevices(ctx context.Context, from, to string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE devices SET template = ? WHERE template = ?`, to, from)
	if err != nil {
		return 0, fmt.Errorf("store: перевод устройств с профиля %q: %w", from, err)
	}
	return res.RowsAffected()
}

// SetDeviceOnline отмечает присутствие устройства на шине.
// Отдельным запросом: это приходит по LWT и не должно затирать остальные поля.
func (s *Store) SetDeviceOnline(ctx context.Context, id int64, online bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE devices SET online = ?, last_seen = ? WHERE id = ?`,
		online, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("store: отметка присутствия: %w", err)
	}
	return checkAffected(res, "устройство", id)
}

// DeleteDevice удаляет устройство вместе с его связками: они заданы внешним
// ключом с каскадом, и осиротевшие связки в базе никому не нужны.
func (s *Store) DeleteDevice(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: удаление устройства: %w", err)
	}
	return checkAffected(res, "устройство", id)
}

func scanDevice(row scanner) (Device, error) {
	var (
		d        Device
		lastSeen sql.NullInt64
	)
	err := row.Scan(&d.ID, &d.Name, &d.TopicPrefix, &d.Model, &d.Template, &d.Online, &lastSeen)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, err
		}
		return Device{}, fmt.Errorf("store: разбор устройства: %w", err)
	}
	if lastSeen.Valid {
		d.LastSeen = time.Unix(lastSeen.Int64, 0)
	}
	return d, nil
}

// nullableTime переводит нулевое время в NULL: ноль unix-времени — это 1970 год,
// и в интерфейсе он выглядел бы как настоящая дата.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}
