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
	"log/slog"
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

	// checkInterval — как часто спрашиваем сами, в фоне. Раз в сутки: релизы
	// выходят реже, а у неавторизованного доступа к API есть предел обращений.
	checkInterval = 24 * time.Hour

	// freshFor — сколько результат считается свежим при заходе на «Обзор».
	// Без этого человек, открывший страницу через час после старта, видел бы
	// вчерашний ответ и не понимал, почему новой версии «нет».
	freshFor = 15 * time.Minute

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
	log     *slog.Logger

	mu       sync.Mutex
	info     Info
	checking bool // проверка уже идёт: два запроса подряд ни к чему
}

// New создаёт проверяльщика для текущей версии.
func New(current string, log *slog.Logger) *Checker {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Checker{
		current: current,
		client:  &http.Client{Timeout: requestTimeout},
		log:     log,
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

// EnsureFresh освежает сведения, если они устарели.
//
// Проверка идёт в стороне и страницу не задерживает: на объекте канал бывает
// узким, а «Обзор» должен открываться сразу. Результат появится к следующему
// заходу — или сразу, если нажать «Проверить».
func (c *Checker) EnsureFresh(ctx context.Context) {
	c.mu.Lock()
	fresh := time.Since(c.info.CheckedAt) < freshFor
	busy := c.checking
	if !fresh && !busy {
		c.checking = true
	}
	c.mu.Unlock()

	if fresh || busy {
		return
	}

	go func() {
		// Свой контекст: запрос переживает закрытие страницы, иначе он
		// обрывался бы на полпути и результат не сохранялся.
		bg, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		c.Check(bg)

		c.mu.Lock()
		c.checking = false
		c.mu.Unlock()
	}()
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
		// Самая частая причина — не права, а ProtectSystem=strict в юните:
		// он закрывает всю файловую систему, кроме перечисленной в
		// ReadWritePaths, и владение каталогом тут не помогает.
		return fmt.Errorf("не удалось записать в %s: %w.\n\n"+
			"Скорее всего каталог закрыт настройкой службы (ProtectSystem=strict). "+
			"Запустите установщик — он добавит каталог в ReadWritePaths:\n"+
			"curl -fsSL https://raw.githubusercontent.com/staskuznec/mqtt2mimismart/main/install.sh | sudo sh",
			dir, err)
	}

	// Прежний сохраняем: откат должен быть в одно движение.
	_ = os.Rename(exe, filepath.Join(dir, "mqtt2mimismart.old"))

	if err := os.Rename(tmp, exe); err != nil {
		return fmt.Errorf("не удалось заменить бинарник: %w", err)
	}

	// Вкладка в панели умного дома едет в том же релизе. Её неудача не
	// отменяет обновления шлюза: он уже заменён и работает.
	if err := c.updatePanelTab(ctx, info.Latest); err != nil {
		c.log.Warn("не удалось обновить вкладку в панели",
			"err", err,
			"как быть", "запустите install.sh — он положит её от root")
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

// panelTabPaths — где может лежать файл вкладки для панели умного дома.
//
// Обновлять его должен шлюз, а не человек: файл едет в том же релизе, и
// требовать ради него отдельного похода в консоль — значит однажды получить
// панель со старой вкладкой и не понять, почему она ведёт себя странно.
var panelTabPaths = []string{
	"/home/html/MimiSetup/mqtt-tab.js",
	"/var/www/html/MimiSetup/mqtt-tab.js",
	"/home/sh2/web/MimiSetup/mqtt-tab.js",
	"/var/www/MimiSetup/mqtt-tab.js",
}

// updatePanelTab обновляет файл вкладки, если он есть и доступен на запись.
//
// Неудача здесь не отменяет обновления шлюза: вкладка — украшение поверх
// работающего шлюза, а не его часть. Поэтому только сообщаем.
func (c *Checker) updatePanelTab(ctx context.Context, version string) error {
	path := ""
	for _, p := range panelTabPaths {
		if _, err := os.Stat(p); err == nil {
			path = p
			break
		}
	}
	if path == "" {
		return nil // панель не найдена — вкладку и не ставили
	}

	url := "https://github.com/" + repo + "/releases/download/" + version + "/mimisetup-mqtt-tab.js"
	body, err := c.download(ctx, url)
	if err != nil {
		return err
	}

	// Прежний адрес шлюза сохраняем: установщик мог подставить туда порт или
	// другой подкаталог, и затирать его значением по умолчанию нельзя.
	if old, err := os.ReadFile(path); err == nil {
		if addr := gatewayURL(string(old)); addr != "" && addr != "/mqtt/" {
			body = []byte(strings.Replace(string(body),
				`var GATEWAY_URL = "/mqtt/";`,
				`var GATEWAY_URL = "`+addr+`";`, 1))
		}
	}

	// Пишем рядом и переименовываем: панель может читать файл прямо сейчас.
	tmp := path + ".new"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// gatewayURL достаёт адрес шлюза из файла вкладки.
func gatewayURL(body string) string {
	const marker = `var GATEWAY_URL = "`
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
