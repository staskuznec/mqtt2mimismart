// Package statecache помнит между запусками, какое значение бинарник уже
// отправил в каждый элемент умного дома.
//
// Бинарник живёт доли секунды и сравнить «было — стало» сам не может, поэтому
// последние отправленные значения складываются в JSON-файл рядом с конфигом.
// Благодаря этому по таймеру уходят только изменившиеся значения: и трафик
// меньше, и обработчик лампы в скрипте не дёргается на каждой минуте.
package statecache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultResendAfter — как часто значение отправляется повторно, даже если
// не изменилось. Страховка от рассинхрона: если кто-то поменял состояние
// элемента в умном доме мимо нас, кэш иначе давил бы исправляющую запись.
const DefaultResendAfter = 10 * time.Minute

// entry — одно запомненное значение.
type entry struct {
	Value  string `json:"v"`
	SentAt int64  `json:"t"` // unix-время последней отправки
}

// Cache — файловый кэш отправленных значений. Не потокобезопасен: бинарник
// однопоточный, а параллельные запуски разводятся по разным ключам.
type Cache struct {
	path        string
	resendAfter time.Duration
	now         time.Time
	values      map[string]entry
	dirty       bool
	disabled    bool
}

// Load читает кэш. Ошибки чтения не критичны: при любой проблеме получаем
// пустой кэш, и первая же отправка уйдёт целиком.
func Load(path string, resendAfter time.Duration) *Cache {
	c := &Cache{
		path:        path,
		resendAfter: resendAfter,
		now:         time.Now(),
		values:      make(map[string]entry),
	}
	if resendAfter <= 0 {
		c.resendAfter = DefaultResendAfter
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var stored map[string]entry
	if err := json.Unmarshal(b, &stored); err == nil && stored != nil {
		c.values = stored
	}
	return c
}

// Disabled возвращает кэш, который ничего не фильтрует и не пишет на диск.
func Disabled() *Cache {
	return &Cache{disabled: true, values: make(map[string]entry)}
}

// Changed сообщает, нужно ли отправлять значение: оно отличается от
// запомненного или слишком давно не отправлялось.
func (c *Cache) Changed(key, value string) bool {
	if c.disabled {
		return true
	}
	prev, ok := c.values[key]
	if !ok || prev.Value != value {
		return true
	}
	return c.now.Sub(time.Unix(prev.SentAt, 0)) >= c.resendAfter
}

// Remember фиксирует, что значение отправлено. На диск попадёт при Save.
func (c *Cache) Remember(key, value string) {
	if c.disabled {
		return
	}
	c.values[key] = entry{Value: value, SentAt: c.now.Unix()}
	c.dirty = true
}

// Save сохраняет кэш, если он менялся. Пишет через временный файл, чтобы
// прерванный запуск не оставил битый JSON.
func (c *Cache) Save() error {
	if c.disabled || !c.dirty {
		return nil
	}

	b, err := json.Marshal(c.values)
	if err != nil {
		return fmt.Errorf("statecache: сериализация: %w", err)
	}

	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("statecache: запись %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("statecache: переименование в %s: %w", c.path, err)
	}
	return nil
}

// PathFor возвращает путь к кэшу рядом с конфигом:
// 93.yaml → 93.state.json.
func PathFor(configPath string) string {
	dir := filepath.Dir(configPath)
	base := filepath.Base(configPath)
	return filepath.Join(dir, strings.TrimSuffix(base, filepath.Ext(base))+".state.json")
}
