package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// HiddenTopic — то, что убрано со страницы «Топики» насовсем.
type HiddenTopic struct {
	// Pattern — либо точный топик, либо мастер-топик устройства.
	Pattern string

	// Tree — скрыт мастер-топик со всем, что под ним. Иначе скрыт ровно один
	// топик: у устройства бывает один болтливый топик при полезных остальных.
	Tree bool

	Since time.Time
}

// HiddenTopics читает список скрытого, свежее сверху.
func (s *Store) HiddenTopics(ctx context.Context) ([]HiddenTopic, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT pattern, whole_tree, created_at FROM hidden_topics ORDER BY created_at DESC, pattern`)
	if err != nil {
		return nil, fmt.Errorf("store: чтение скрытых топиков: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []HiddenTopic
	for rows.Next() {
		var (
			h    HiddenTopic
			tree int
			at   int64
		)
		if err := rows.Scan(&h.Pattern, &tree, &at); err != nil {
			return nil, fmt.Errorf("store: чтение скрытых топиков: %w", err)
		}
		h.Tree, h.Since = tree != 0, time.Unix(at, 0)
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: чтение скрытых топиков: %w", err)
	}
	return out, nil
}

// HideTopic заносит топик или мастер-топик в скрытые.
//
// Повторное скрытие того же топика — не ошибка: страница живая, и человек
// вправе нажать кнопку дважды, не разбираясь, успело ли примениться первое
// нажатие. Ширина правила при этом обновляется: скрыть отдельный топик, а
// потом всё устройство — обычный ход.
func (s *Store) HideTopic(ctx context.Context, pattern string, tree bool) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return fmt.Errorf("store: скрытие топика: пустой топик")
	}

	whole := 0
	if tree {
		whole = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO hidden_topics (pattern, whole_tree, created_at) VALUES (?, ?, ?)
		ON CONFLICT(pattern) DO UPDATE SET whole_tree = excluded.whole_tree`,
		pattern, whole, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("store: скрытие топика %q: %w", pattern, err)
	}
	return nil
}

// ShowTopic убирает топик из скрытых — он снова начнёт запоминаться.
func (s *Store) ShowTopic(ctx context.Context, pattern string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM hidden_topics WHERE pattern = ?`, pattern); err != nil {
		return fmt.Errorf("store: возврат топика %q: %w", pattern, err)
	}
	return nil
}
