package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Ключи настроек. Значения вводятся в веб-интерфейсе, конфигов на диске нет.
const (
	KeyMQTTAddr     = "mqtt.addr"      // host:port брокера
	KeyMQTTUser     = "mqtt.user"      //
	KeyMQTTPassword = "mqtt.password"  //
	KeyMQTTClientID = "mqtt.client_id" // пусто — соберётся из имени хоста

	KeySHSAddr = "shs.addr" // host:port сервера умного дома
	KeySHSKey  = "shs.key"  // ключ AES: ровно 16, 24 или 32 байта
	KeySHSMac  = "shs.mac"  // mac-id клиента; пусто — сгенерируется случайный
)

// Config — подключения, которые шлюз поднимает при старте.
//
// Плоская структура, а не дерево: это набор полей одной формы в вебе, и так
// его проще и заполнять, и сохранять одной транзакцией.
type Config struct {
	MQTTAddr     string
	MQTTUser     string
	MQTTPassword string
	MQTTClientID string

	SHSAddr string
	SHSKey  string
	SHSMac  string
}

// Ready сообщает, что настроек достаточно для запуска обеих сторон шлюза.
// Пока это не так, веб показывает мастер первого запуска.
func (c Config) Ready() bool {
	return c.MQTTAddr != "" && c.SHSAddr != "" && c.SHSKey != ""
}

// ErrKeyLength — ключ SHS неподходящей длины.
var ErrKeyLength = errors.New("ключ должен быть длиной ровно 16, 24 или 32 байта")

// Validate проверяет то, что можно проверить без сети.
//
// Длину ключа проверяем здесь намеренно: это требование AES, и клиент SHS с
// неверной длиной не стартует вовсе. Поймать это при сохранении формы куда
// понятнее, чем потом читать «invalid key» в журнале.
//
// Длина считается в байтах, а не в символах, — и это правильно: AES меряет
// именно байты. Побочный эффект в том, что ключ из восьми кириллических букв
// занимает 16 байт и проверку проходит.
func (c Config) Validate() error {
	if c.SHSKey != "" {
		switch len(c.SHSKey) {
		case 16, 24, 32:
		default:
			return fmt.Errorf("shs.key: %w (сейчас %d)", ErrKeyLength, len(c.SHSKey))
		}
	}
	if c.MQTTAddr != "" && !strings.Contains(c.MQTTAddr, ":") {
		return fmt.Errorf("mqtt.addr: ожидался host:port, получено %q", c.MQTTAddr)
	}
	if c.SHSAddr != "" && !strings.Contains(c.SHSAddr, ":") {
		return fmt.Errorf("shs.addr: ожидался host:port, получено %q", c.SHSAddr)
	}
	return nil
}

// Get читает одну настройку. Второе значение сообщает, задана ли она вообще:
// пустая строка и отсутствие ключа — разные вещи.
func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("store: чтение настройки %q: %w", key, err)
	}
	return value, true, nil
}

// Set записывает одну настройку.
func (s *Store) Set(ctx context.Context, key, value string) error {
	return s.SetMany(ctx, map[string]string{key: value})
}

// SetMany записывает набор настроек одной транзакцией: форма в вебе
// сохраняется целиком либо не сохраняется вовсе.
func (s *Store) SetMany(ctx context.Context, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: запись настроек: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`)
	if err != nil {
		return fmt.Errorf("store: запись настроек: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	now := time.Now().Unix()
	for key, value := range values {
		if strings.TrimSpace(key) == "" {
			return errors.New("store: пустой ключ настройки")
		}
		if _, err := stmt.ExecContext(ctx, key, value, now); err != nil {
			return fmt.Errorf("store: запись настройки %q: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: запись настроек: %w", err)
	}
	return nil
}

// All читает все настройки разом.
func (s *Store) All(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("store: чтение настроек: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("store: чтение настроек: %w", err)
		}
		out[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: чтение настроек: %w", err)
	}
	return out, nil
}

// Config собирает настройки подключений в типизированный вид.
func (s *Store) Config(ctx context.Context) (Config, error) {
	all, err := s.All(ctx)
	if err != nil {
		return Config{}, err
	}
	return Config{
		MQTTAddr:     all[KeyMQTTAddr],
		MQTTUser:     all[KeyMQTTUser],
		MQTTPassword: all[KeyMQTTPassword],
		MQTTClientID: all[KeyMQTTClientID],
		SHSAddr:      all[KeySHSAddr],
		SHSKey:       all[KeySHSKey],
		SHSMac:       all[KeySHSMac],
	}, nil
}

// SaveConfig проверяет и сохраняет настройки подключений.
func (s *Store) SaveConfig(ctx context.Context, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return s.SetMany(ctx, map[string]string{
		KeyMQTTAddr:     c.MQTTAddr,
		KeyMQTTUser:     c.MQTTUser,
		KeyMQTTPassword: c.MQTTPassword,
		KeyMQTTClientID: c.MQTTClientID,
		KeySHSAddr:      c.SHSAddr,
		KeySHSKey:       c.SHSKey,
		KeySHSMac:       c.SHSMac,
	})
}
