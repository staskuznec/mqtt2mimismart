package logging

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Записей в памяти. Тысячи хватает, чтобы увидеть, что происходило последние
// минуты, — а именно это и нужно, когда что-то не работает прямо сейчас.
// Полная история живёт в journald, подменять его незачем.
const bufferSize = 1000

// Entry — одна запись журнала для показа в вебе.
type Entry struct {
	At      time.Time
	Level   slog.Level
	Message string
	Attrs   map[string]string
}

// Ring — кольцо последних записей, общее на всё приложение.
//
// Нужно ради одного: когда шлюз работает службой, увидеть причину неполадки
// без похода в консоль. Человек, настраивающий связки в браузере, не обязан
// знать про journalctl, а понять, почему не подключается брокер, должен.
type Ring struct {
	mu      sync.RWMutex
	entries []Entry
	next    int
	full    bool
}

// NewRing создаёт кольцо.
func NewRing() *Ring {
	return &Ring{entries: make([]Entry, bufferSize)}
}

func (r *Ring) add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.entries[r.next] = e
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.full = true
	}
}

// Entries отдаёт записи, новые сверху. Не больше limit; ноль — все.
func (r *Ring) Entries(limit int, minLevel slog.Level) []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := len(r.entries)
	if !r.full {
		count = r.next
	}

	out := make([]Entry, 0, count)
	for i := 0; i < count; i++ {
		// Идём назад от последней записи: свежие нужны первыми.
		idx := (r.next - 1 - i + len(r.entries)) % len(r.entries)
		e := r.entries[idx]
		if e.At.IsZero() || e.Level < minLevel {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// recorder — обработчик slog, складывающий записи в кольцо и передающий их
// дальше. Подчинённые обработчики делят одно кольцо: записи от разных
// компонентов должны сходиться в общий журнал, а не расползаться.
type recorder struct {
	next  slog.Handler
	ring  *Ring
	attrs []slog.Attr
}

var _ slog.Handler = (*recorder)(nil)

func (r *recorder) Enabled(ctx context.Context, level slog.Level) bool {
	return r.next.Enabled(ctx, level)
}

func (r *recorder) Handle(ctx context.Context, rec slog.Record) error {
	e := Entry{
		At:      rec.Time,
		Level:   rec.Level,
		Message: rec.Message,
		Attrs:   make(map[string]string, rec.NumAttrs()+len(r.attrs)),
	}
	for _, a := range r.attrs {
		e.Attrs[a.Key] = a.Value.String()
	}
	rec.Attrs(func(a slog.Attr) bool {
		e.Attrs[a.Key] = a.Value.String()
		return true
	})

	r.ring.add(e)
	return r.next.Handle(ctx, rec)
}

func (r *recorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &recorder{
		next:  r.next.WithAttrs(attrs),
		ring:  r.ring,
		attrs: append(append([]slog.Attr{}, r.attrs...), attrs...),
	}
}

func (r *recorder) WithGroup(name string) slog.Handler {
	return &recorder{next: r.next.WithGroup(name), ring: r.ring, attrs: r.attrs}
}
