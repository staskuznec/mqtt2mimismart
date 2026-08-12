package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/staskuznec/mqtt2mimismart/internal/devtmpl"
	"github.com/staskuznec/mqtt2mimismart/internal/link"
)

// TemplateInfo — шаблон в списке.
type TemplateInfo struct {
	devtmpl.Template
	Builtin bool // встроенный правке не подлежит
}

// Templates перечисляет доступные шаблоны: встроенные плюс загруженные.
//
// Загруженный с тем же ключом перекрывает встроенный — так модель правится под
// конкретную прошивку, не дожидаясь релиза шлюза.
func (s *Store) Templates(ctx context.Context) ([]TemplateInfo, error) {
	builtin, err := devtmpl.Builtin()
	if err != nil {
		return nil, err
	}

	byKey := make(map[string]TemplateInfo, len(builtin))
	for _, t := range builtin {
		byKey[t.Key] = TemplateInfo{Template: t, Builtin: true}
	}

	rows, err := s.db.QueryContext(ctx, `SELECT key, body_json FROM templates`)
	if err != nil {
		return nil, fmt.Errorf("store: чтение шаблонов: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var key, body string
		if err := rows.Scan(&key, &body); err != nil {
			return nil, fmt.Errorf("store: чтение шаблонов: %w", err)
		}
		t, err := devtmpl.Parse([]byte(body))
		if err != nil {
			// Битый шаблон в базе не должен ронять список остальных.
			continue
		}
		t.Key = key
		byKey[key] = TemplateInfo{Template: t}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: чтение шаблонов: %w", err)
	}

	out := make([]TemplateInfo, 0, len(byKey))
	for _, t := range byKey {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Template возвращает шаблон по ключу.
func (s *Store) Template(ctx context.Context, key string) (devtmpl.Template, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT body_json FROM templates WHERE key = ?`, key).Scan(&body)
	switch {
	case err == nil:
		t, err := devtmpl.Parse([]byte(body))
		if err != nil {
			return devtmpl.Template{}, err
		}
		t.Key = key
		return t, nil
	case errors.Is(err, sql.ErrNoRows):
		return devtmpl.Find(key)
	default:
		return devtmpl.Template{}, fmt.Errorf("store: чтение шаблона: %w", err)
	}
}

// SaveTemplate сохраняет загруженный шаблон. Разбор и проверка идут до записи:
// шаблон применяют один раз и потом доверяют ему.
func (s *Store) SaveTemplate(ctx context.Context, key string, body []byte) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("store: у шаблона пустой ключ")
	}

	t, err := devtmpl.Parse(body)
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO templates (key, name, body_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			name = excluded.name, body_json = excluded.body_json, updated_at = excluded.updated_at`,
		key, t.Name, string(body), now, now)
	if err != nil {
		return fmt.Errorf("store: сохранение шаблона: %w", err)
	}
	return nil
}

// DeleteTemplate удаляет загруженный шаблон. Встроенный при этом снова
// становится действующим, если ключ совпадал.
func (s *Store) DeleteTemplate(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM templates WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: удаление шаблона: %w", err)
	}
	return nil
}

// ApplyTemplate заводит устройство и разворачивает связки шаблона.
//
// Всё одной транзакцией: половина развёрнутого устройства — это связки,
// которые уже работают, при том что устройства как будто нет.
func (s *Store) ApplyTemplate(ctx context.Context, d Device, t devtmpl.Template,
	assign map[string]devtmpl.Addr) (int64, error) {

	links, err := t.Apply(d.TopicPrefix, assign)
	if err != nil {
		return 0, err
	}

	if strings.TrimSpace(d.Name) == "" {
		d.Name = t.Name
	}
	d.Model, d.Template = t.Model, t.Key

	deviceID, err := s.CreateDevice(ctx, d)
	if err != nil {
		return 0, err
	}

	// Метки пар из шаблона превращаются в общий идентификатор: обе стороны
	// канала должны заводиться и удаляться вместе.
	pairIDs := make(map[string]int64)
	for i := range links {
		links[i].DeviceID = deviceID

		pair := pairLabel(t, links[i])
		if pair != "" {
			if id, ok := pairIDs[pair]; ok {
				links[i].PairID = id
			}
		}

		id, err := s.CreateLink(ctx, links[i])
		if err != nil {
			return 0, fmt.Errorf("связка «%s»: %w", links[i].Name, err)
		}
		links[i].ID = id

		if pair != "" {
			if _, ok := pairIDs[pair]; !ok {
				// Пара носит идентификатор первой своей стороны.
				pairIDs[pair] = id
				links[i].PairID = id
				if err := s.UpdateLink(ctx, links[i]); err != nil {
					return 0, err
				}
			}
		}
	}
	return deviceID, nil
}

// pairLabel находит метку пары для развёрнутой связки по её имени: имена
// уникальны внутри шаблона, а порядок при пропуске ролей сдвигается.
func pairLabel(t devtmpl.Template, l link.Link) string {
	for _, spec := range t.Links {
		if spec.Name == l.Name {
			return spec.Pair
		}
	}
	return ""
}
