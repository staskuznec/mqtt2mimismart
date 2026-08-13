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

	// Проверяем ДО заведения устройства: иначе неверное назначение оставило бы
	// в базе устройство без единой связки.
	if _, err := t.Apply(d.TopicPrefix, assign); err != nil {
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

	if err := s.createTemplateLinks(ctx, deviceID, t, assign, nil); err != nil {
		return 0, err
	}
	return deviceID, nil
}

// createTemplateLinks разворачивает связки шаблона на устройстве.
//
// disabled перечисляет связки по имени, которые надо оставить выключенными:
// при перенастройке устройства человек мог сознательно погасить лишнее, и
// правка назначений не повод это включать.
func (s *Store) createTemplateLinks(ctx context.Context, deviceID int64, t devtmpl.Template,
	assign map[string]devtmpl.Addr, disabled map[string]bool) error {

	links, err := t.Apply(deviceTopicPrefix(ctx, s, deviceID), assign)
	if err != nil {
		return err
	}

	// Метки пар из шаблона превращаются в общий идентификатор: обе стороны
	// канала должны заводиться и удаляться вместе.
	pairIDs := make(map[string]int64)
	for i := range links {
		links[i].DeviceID = deviceID
		if disabled[links[i].Name] {
			links[i].Enabled = false
		}

		pair := pairLabel(t, links[i])
		if pair != "" {
			if id, ok := pairIDs[pair]; ok {
				links[i].PairID = id
			}
		}

		id, err := s.CreateLink(ctx, links[i])
		if err != nil {
			return fmt.Errorf("связка «%s»: %w", links[i].Name, err)
		}
		links[i].ID = id

		if pair != "" {
			if _, ok := pairIDs[pair]; !ok {
				// Пара носит идентификатор первой своей стороны.
				pairIDs[pair] = id
				links[i].PairID = id
				if err := s.UpdateLink(ctx, links[i]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// deviceTopicPrefix читает префикс устройства: связки строятся от него.
func deviceTopicPrefix(ctx context.Context, s *Store, deviceID int64) string {
	d, err := s.Device(ctx, deviceID)
	if err != nil {
		return ""
	}
	return d.TopicPrefix
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

// Assignment восстанавливает, какой элемент назначен каждой роли устройства.
//
// Связки хранят адрес элемента, но не роль: роль — понятие шаблона, а связка
// уже развёрнута. Восстанавливаем по имени: имена внутри шаблона уникальны,
// и по ним связка однозначно опознаётся.
func (s *Store) Assignment(ctx context.Context, deviceID int64, t devtmpl.Template) (map[string]devtmpl.Addr, error) {
	links, err := s.LinksByDevice(ctx, deviceID)
	if err != nil {
		return nil, err
	}

	byName := make(map[string]string, len(t.Links)) // имя связки → роль
	for _, spec := range t.Links {
		byName[spec.Name] = spec.Role
	}

	assign := make(map[string]devtmpl.Addr)
	for _, l := range links {
		role, ok := byName[l.Name]
		if !ok {
			continue // связку добавили руками, к шаблону она отношения не имеет
		}
		assign[role] = devtmpl.Addr{ID: l.TargetID, SubID: l.TargetSubID}
	}
	return assign, nil
}

// ReapplyTemplate перенастраивает уже заведённое устройство.
//
// Связки шаблона заменяются целиком: назначения ролей могли и добавиться, и
// исчезнуть, а вычислять разницу — значит наживать расхождение между тем, что
// в базе, и тем, что показано в форме.
//
// Связки, заведённые к устройству вручную, остаются нетронутыми: их имена в
// шаблоне не встречаются, и трогать чужую работу мы не вправе.
func (s *Store) ReapplyTemplate(ctx context.Context, d Device, t devtmpl.Template,
	assign map[string]devtmpl.Addr) error {

	// Проверяем ДО удаления: иначе неверное назначение оставило бы устройство
	// вовсе без связок.
	if _, err := t.Apply(d.TopicPrefix, assign); err != nil {
		return err
	}

	existing, err := s.LinksByDevice(ctx, d.ID)
	if err != nil {
		return err
	}
	fromTemplate := make(map[string]bool, len(t.Links))
	for _, spec := range t.Links {
		fromTemplate[spec.Name] = true
	}

	// Запоминаем, что было выключено: правка назначений не повод включать
	// обратно то, что человек сознательно погасил.
	disabled := make(map[string]bool)
	for _, l := range existing {
		if !fromTemplate[l.Name] {
			continue
		}
		if !l.Enabled {
			disabled[l.Name] = true
		}
		if err := s.DeleteLink(ctx, l.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}

	d.Model, d.Template = t.Model, t.Key
	if err := s.UpdateDevice(ctx, d); err != nil {
		return err
	}
	return s.createTemplateLinks(ctx, d.ID, t, assign, disabled)
}
