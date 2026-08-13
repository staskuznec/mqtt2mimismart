// Package update проверяет, вышла ли новая версия шлюза.
//
// Только проверка: скачиванием и заменой бинарника занимается install.sh,
// запущенный человеком. Демон, который управляет светом в доме, не должен
// подменять сам себя посреди дня — а вот сказать, что вышло обновление, он
// обязан, иначе о нём никто не узнает.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// repo — где живут релизы.
	repo = "staskuznec/mqtt2mimismart"

	// releasesURL — публичный API GitHub, без ключа и без учётной записи.
	releasesURL = "https://api.github.com/repos/" + repo + "/releases/latest"

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

// Apply скачивает бинарник нужной версии и заменяет им работающий.
//
// Демон после этого должен завершиться, а поднять его обратно — дело systemd.
// Подменять себя на ходу нельзя: работающий процесс продолжает жить со старым
// кодом, и пока он не перезапустится, ничего не меняется.
//
// Сверка контрольной суммы обязательна: дальше этот файл запускается как
// служба, и без неё это было бы «выполнить то, что пришло».
func (c *Checker) Apply(ctx context.Context) error {
	info := c.Info()
	if info.Latest == "" {
		info = c.Check(ctx)
	}
	if !info.Available {
		return fmt.Errorf("обновляться не на что: установлена %s", info.Current)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("не удалось определить свой путь: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	asset := "mqtt2mimismart-" + assetSuffix()
	base := "https://github.com/" + repo + "/releases/download/" + info.Latest

	binary, err := c.download(ctx, base+"/"+asset)
	if err != nil {
		return err
	}

	sums, err := c.download(ctx, base+"/SHA256SUMS")
	if err != nil {
		return fmt.Errorf("не удалось получить контрольные суммы: %w", err)
	}
	want, err := sumFor(sums, asset)
	if err != nil {
		return err
	}
	got := fmt.Sprintf("%x", sha256.Sum256(binary))
	if got != want {
		return fmt.Errorf("контрольная сумма не сошлась: файл повреждён или подменён")
	}

	// Пишем рядом и переименовываем: замена в пределах каталога атомарна, и
	// служба не увидит наполовину записанный файл.
	dir := filepath.Dir(exe)
	tmp := filepath.Join(dir, ".mqtt2mimismart.new")
	if err := os.WriteFile(tmp, binary, 0o755); err != nil {
		return fmt.Errorf("не удалось записать новый бинарник: %w", err)
	}

	// Прежний сохраняем: откат должен быть в одно движение.
	_ = os.Rename(exe, filepath.Join(dir, "mqtt2mimismart.old"))

	if err := os.Rename(tmp, exe); err != nil {
		return fmt.Errorf("не удалось заменить бинарник: %w", err)
	}
	return nil
}

// download качает файл целиком.
func (c *Checker) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	// Скачивание дольше проверки версии: бинарник весит около десяти мегабайт,
	// а канал на объекте бывает узким.
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("не удалось скачать %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("сервер ответил %s на %s", resp.Status, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// assetSuffix — имя файла релиза под текущую платформу.
func assetSuffix() string {
	switch runtime.GOARCH {
	case "arm":
		return "linux-armv7"
	case "arm64":
		return "linux-arm64"
	default:
		return "linux-amd64"
	}
}

// sumFor достаёт из SHA256SUMS сумму нужного файла.
func sumFor(sums []byte, asset string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("в SHA256SUMS нет строки для %s", asset)
}
