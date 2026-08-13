// Package update проверяет, вышла ли новая версия шлюза.
//
// Только проверка: скачиванием и заменой бинарника занимается install.sh,
// запущенный человеком. Демон, который управляет светом в доме, не должен
// подменять сам себя посреди дня — а вот сказать, что вышло обновление, он
// обязан, иначе о нём никто не узнает.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// releasesURL — публичный API GitHub, без ключа и без учётной записи.
	releasesURL = "https://api.github.com/repos/staskuznec/mqtt2mimismart/releases/latest"

	// checkInterval — как часто спрашиваем. Раз в сутки: релизы выходят реже,
	// а у неавторизованного доступа к API есть предел обращений.
	checkInterval = 24 * time.Hour

	requestTimeout = 10 * time.Second
)

// Info — что известно об обновлении.
type Info struct {
	Current   string    // версия, которая работает сейчас
	Latest    string    // последняя опубликованная
	Available bool      // есть ли смысл обновляться
	URL       string    // страница релиза
	CheckedAt time.Time //
	Error     string    // почему не удалось проверить
}

// Checker периодически спрашивает GitHub о новых версиях.
type Checker struct {
	current string
	client  *http.Client

	mu   sync.Mutex
	info Info
}

// New создаёт проверяльщика для текущей версии.
func New(current string) *Checker {
	return &Checker{
		current: current,
		client:  &http.Client{Timeout: requestTimeout},
		info:    Info{Current: current},
	}
}

// Info отдаёт последний результат проверки.
func (c *Checker) Info() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

// Run проверяет обновления при старте и дальше раз в сутки.
func (c *Checker) Run(ctx context.Context) {
	// Первая проверка не сразу: при старте демону есть чем заняться, а
	// интернета на объекте может не быть вовсе.
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			c.Check(ctx)
			timer.Reset(checkInterval)
		}
	}
}

// Check спрашивает GitHub о последней версии.
func (c *Checker) Check(ctx context.Context) Info {
	info := Info{Current: c.current, CheckedAt: time.Now()}

	latest, url, err := c.fetch(ctx)
	if err != nil {
		// Отсутствие интернета — обычное дело на объекте, и ошибкой работы
		// шлюза это не является: просто сообщаем и живём дальше.
		info.Error = err.Error()
	} else {
		info.Latest, info.URL = latest, url
		info.Available = Newer(c.current, latest)
	}

	c.mu.Lock()
	c.info = info
	c.mu.Unlock()
	return info
}

func (c *Checker) fetch(ctx context.Context) (version, url string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("не удалось спросить GitHub: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("GitHub ответил %s", resp.Status)
	}

	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", "", fmt.Errorf("ответ GitHub не разобрался: %w", err)
	}
	if release.TagName == "" {
		return "", "", fmt.Errorf("в ответе GitHub нет номера версии")
	}
	return release.TagName, release.HTMLURL, nil
}

// Newer сравнивает версии вида "v1.2.3" или "1.2.3".
//
// Собранная из git-описания версия ("f24ced0-dirty") номером не является:
// сравнивать её не с чем, и предлагать обновление по ней нельзя — иначе
// разработочная сборка вечно считалась бы устаревшей.
func Newer(current, latest string) bool {
	cur, okCur := parse(current)
	lat, okLat := parse(latest)
	if !okCur || !okLat {
		return false
	}
	for i := 0; i < 3; i++ {
		if lat[i] != cur[i] {
			return lat[i] > cur[i]
		}
	}
	return false
}

// parse разбирает "v1.2.3" в тройку чисел.
func parse(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Отбрасываем всё после номера: "1.2.3-rc1", "1.2.3+abc".
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}

	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}

	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
