package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/staskuznec/mqtt2mimismart/internal/link"
)

// ErrNotFound — записи с таким идентификатором нет.
var ErrNotFound = errors.New("store: запись не найдена")

// linkColumns перечисляются явно, а не через "*": порядок столбцов в SELECT
// должен совпадать с порядком в Scan, и звёздочка это молча ломает при любой
// новой миграции.
const linkColumns = `id, device_id, name, enabled, direction, topic, qos, retain,
	extract, extract_path, values_json, scale, offset_value,
	target_id, target_subid, encode, decode, unit, precision, only_changed`

// Links читает все связки, сначала включённые, потом по адресу элемента.
func (s *Store) Links(ctx context.Context) ([]link.Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+linkColumns+` FROM links ORDER BY direction, target_id, target_subid, id`)
	if err != nil {
		return nil, fmt.Errorf("store: чтение связок: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []link.Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: чтение связок: %w", err)
	}
	return out, nil
}

// Link читает одну связку.
func (s *Store) Link(ctx context.Context, id int64) (link.Link, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+linkColumns+` FROM links WHERE id = ?`, id)
	l, err := scanLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return link.Link{}, fmt.Errorf("%w: связка %d", ErrNotFound, id)
	}
	return l, err
}

// LinksByDevice читает связки одного устройства.
func (s *Store) LinksByDevice(ctx context.Context, deviceID int64) ([]link.Link, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+linkColumns+` FROM links WHERE device_id = ? ORDER BY direction, id`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("store: чтение связок устройства: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []link.Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CreateLink сохраняет новую связку и возвращает её идентификатор.
//
// Проверка идёт до записи: связка, которая заведомо не заработает, не должна
// попадать в базу — иначе движок будет спотыкаться о неё на каждом сообщении,
// а в вебе она будет выглядеть настроенной.
func (s *Store) CreateLink(ctx context.Context, l link.Link) (int64, error) {
	if err := l.Validate(); err != nil {
		return 0, err
	}

	values, err := marshalValues(l.Values)
	if err != nil {
		return 0, err
	}

	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO links (device_id, name, enabled, direction, topic, qos, retain,
			extract, extract_path, values_json, scale, offset_value,
			target_id, target_subid, encode, decode, unit, precision, only_changed,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableID(l.DeviceID), l.Name, l.Enabled, string(l.Direction), l.Topic, l.QoS, l.Retain,
		l.Extract, l.ExtractPath, values, l.Scale, l.Offset,
		l.TargetID, l.TargetSubID, l.Encode, l.Decode, l.Unit, l.Precision, l.OnlyChanged,
		now, now)
	if err != nil {
		return 0, fmt.Errorf("store: сохранение связки: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: сохранение связки: %w", err)
	}
	return id, nil
}

// UpdateLink перезаписывает связку целиком.
func (s *Store) UpdateLink(ctx context.Context, l link.Link) error {
	if err := l.Validate(); err != nil {
		return err
	}

	values, err := marshalValues(l.Values)
	if err != nil {
		return err
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE links SET device_id = ?, name = ?, enabled = ?, direction = ?, topic = ?,
			qos = ?, retain = ?, extract = ?, extract_path = ?, values_json = ?,
			scale = ?, offset_value = ?, target_id = ?, target_subid = ?,
			encode = ?, decode = ?, unit = ?, precision = ?, only_changed = ?,
			updated_at = ?
		WHERE id = ?`,
		nullableID(l.DeviceID), l.Name, l.Enabled, string(l.Direction), l.Topic,
		l.QoS, l.Retain, l.Extract, l.ExtractPath, values,
		l.Scale, l.Offset, l.TargetID, l.TargetSubID,
		l.Encode, l.Decode, l.Unit, l.Precision, l.OnlyChanged,
		time.Now().Unix(), l.ID)
	if err != nil {
		return fmt.Errorf("store: обновление связки: %w", err)
	}
	return checkAffected(res, "связка", l.ID)
}

// SetLinkEnabled включает или выключает связку, не трогая остальных полей.
// Отдельным запросом, потому что это самое частое действие в таблице связок.
func (s *Store) SetLinkEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE links SET enabled = ?, updated_at = ? WHERE id = ?`,
		enabled, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("store: переключение связки: %w", err)
	}
	return checkAffected(res, "связка", id)
}

// DeleteLink удаляет связку.
func (s *Store) DeleteLink(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM links WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: удаление связки: %w", err)
	}
	return checkAffected(res, "связка", id)
}

// scanner объединяет *sql.Row и *sql.Rows: разбор строки у них одинаковый.
type scanner interface {
	Scan(dest ...any) error
}

// scanLink разбирает строку в связку.
func scanLink(row scanner) (link.Link, error) {
	var (
		l         link.Link
		deviceID  sql.NullInt64
		direction string
		values    string
		precision sql.NullInt64
	)

	err := row.Scan(&l.ID, &deviceID, &l.Name, &l.Enabled, &direction, &l.Topic, &l.QoS, &l.Retain,
		&l.Extract, &l.ExtractPath, &values, &l.Scale, &l.Offset,
		&l.TargetID, &l.TargetSubID, &l.Encode, &l.Decode, &l.Unit, &precision, &l.OnlyChanged)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return link.Link{}, err
		}
		return link.Link{}, fmt.Errorf("store: разбор связки: %w", err)
	}

	l.DeviceID = deviceID.Int64
	l.Direction = link.Direction(direction)
	if precision.Valid {
		p := int(precision.Int64)
		l.Precision = &p
	}
	if l.Values, err = unmarshalValues(values); err != nil {
		return link.Link{}, fmt.Errorf("store: связка %d: %w", l.ID, err)
	}
	return l, nil
}

// marshalValues превращает таблицу значений в JSON. Пустая таблица хранится
// пустой строкой, а не "{}": так её проще отличить глазами при отладке.
func marshalValues(values map[string]string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("store: таблица значений: %w", err)
	}
	return string(b), nil
}

func unmarshalValues(s string) (map[string]string, error) {
	if s == "" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(s), &values); err != nil {
		return nil, fmt.Errorf("таблица значений не разбирается: %w", err)
	}
	return values, nil
}

// nullableID переводит нулевой идентификатор в NULL: внешний ключ на
// несуществующее устройство с нулём не пройдёт проверку целостности.
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// checkAffected превращает «ноль изменённых строк» в понятную ошибку.
func checkAffected(res sql.Result, what string, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s %d: %w", what, id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s %d", ErrNotFound, what, id)
	}
	return nil
}
