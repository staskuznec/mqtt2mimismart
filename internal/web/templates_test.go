package web

import (
	"bytes"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/staskuznec/mqtt2mimismart/internal/devtmpl"
	"github.com/staskuznec/mqtt2mimismart/internal/logic"
	"github.com/staskuznec/mqtt2mimismart/internal/mqtt"
	"github.com/staskuznec/mqtt2mimismart/internal/shs"
	"github.com/staskuznec/mqtt2mimismart/internal/store"
	"github.com/staskuznec/mqtt2mimismart/internal/update"
)

// Абсолютный адрес в шаблоне ломает работу за прокси: шлюз стоит в подкаталоге
// (http://сервер/mqtt/), и ссылка от корня уводит на чужую страницу — сервер
// отвечает «Not Found». Ловится это только руками и только на живом сервере,
// поэтому проверяем сборкой.
//
// Разрешён единственный абсолютный адрес — сам тег base, он и задаёт подкаталог.
func TestTemplatesUseRelativeLinks(t *testing.T) {
	attr := regexp.MustCompile(`(href|action)="([^"]*)"`)

	err := fs.WalkDir(templateFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		body, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}

		for _, m := range attr.FindAllStringSubmatch(string(body), -1) {
			value := m[2]

			// Тег base — единственное место, где абсолютный путь уместен.
			if strings.Contains(value, "{{base}}") {
				continue
			}
			// Чужие адреса вроде "https://..." нас не касаются.
			if strings.Contains(value, "://") {
				continue
			}

			if strings.HasPrefix(value, "/") {
				t.Errorf("%s: %s=%q начинается со слэша — за прокси уведёт мимо подкаталога",
					path, m[1], value)
			}
			// Слэш сразу после шаблонной вставки — тот же абсолютный адрес,
			// просто собранный по частям: {{if …}}/devices/… .
			if regexp.MustCompile(`\{\{[^}]*\}\}/[a-z]`).MatchString(value) &&
				!strings.Contains(value, "{{.") {
				t.Errorf("%s: %s=%q — абсолютный адрес внутри условия",
					path, m[1], value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход шаблонов: %v", err)
	}
}

// Обращения к серверу из скриптов тоже должны быть относительными.
func TestTemplatesUseRelativeFetch(t *testing.T) {
	bad := regexp.MustCompile(`fetch\(['"]/`)

	err := fs.WalkDir(templateFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		body, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}
		if bad.Match(body) {
			t.Errorf("%s: запрос от корня — за прокси не найдёт обработчик", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход шаблонов: %v", err)
	}
}

// Каждая страница должна разбираться: битый шаблон обнаруживается только при
// открытии страницы, то есть уже на объекте.
func TestAllPagesParse(t *testing.T) {
	pages := buildPages("/mqtt")
	if len(pages) == 0 {
		t.Fatal("страниц не собрано вовсе")
	}
	for name, tmpl := range pages {
		if tmpl == nil {
			t.Errorf("страница %q не разобрана", name)
		}
	}
}

// Блок шаблона должен закрываться в самом конце файла.
//
// Дописанное после закрывающего {{end}} в страницу не попадает вовсе: разбор
// проходит, тесты молчат, а на сервере не работает живое обновление — и понять
// почему неоткуда. Ровно так дважды уезжал скрипт опроса.
func TestTemplateBlocksCloseAtEnd(t *testing.T) {
	opens := regexp.MustCompile(`\{\{\s*(define|if|with|range|block)\b`)
	closes := regexp.MustCompile(`\{\{\s*end\s*\}\}`)

	err := fs.WalkDir(templateFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		body, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}

		lines := strings.Split(string(body), "\n")
		depth, closedAt := 0, -1
		for i, line := range lines {
			depth += len(opens.FindAllString(line, -1)) - len(closes.FindAllString(line, -1))
			if depth == 0 && closedAt < 0 {
				closedAt = i + 1
			}
			if depth < 0 {
				t.Fatalf("%s, строка %d: лишний {{end}}", path, i+1)
			}
		}

		if depth != 0 {
			t.Errorf("%s: не закрыто блоков: %d", path, depth)
			return nil
		}
		// Хвост из пустых строк допустим, содержимое — нет.
		for i := closedAt; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) != "" {
				t.Errorf("%s: строка %d вне блока — в страницу она не попадёт: %.60s",
					path, i+1, strings.TrimSpace(lines[i]))
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход шаблонов: %v", err)
	}
}

// Страницы должны не только разбираться, но и отрисовываться.
//
// Разбор проверяет синтаксис, а обращение к несуществующему полю обнаруживается
// только при выполнении: отрисовка обрывается на середине, страница приходит
// урезанной, и в браузере это выглядит как «список из одного пункта, дальше
// ничего». Ровно так уехало обращение к .Builtin вместо .Bundled.
func TestPagesRender(t *testing.T) {
	precision := 1
	item := devtmpl.Item{
		Template: devtmpl.Template{
			Key: "sample", Name: "Пример", Model: "X", Note: "заметка",
			Roles: []devtmpl.Role{{Key: "ch0", Title: "Канал", Required: true}},
			Links: []devtmpl.LinkSpec{{
				Name: "Состояние", Direction: "in", Role: "ch0",
				Topic: "{{prefix}}/relay/0", Encode: "byte", Precision: &precision,
			}},
		},
		Bundled: true,
	}
	element := elementOption{Addr: "758:4", Label: "Прихожая → Свет (lamp)", Type: "lamp"}

	mqttStatus := mqtt.Status{Connected: true, ClientID: "gw", KnownTopics: 3}
	shsStatus := shs.Status{Phase: shs.PhaseConnected, ClientID: 2031}
	upd := update.Info{Current: "v1.0.0", Latest: "v1.1.0", Available: true, URL: "https://example"}

	cases := map[string]any{
		"overview": overviewData{
			Title: "Обзор", Nav: "overview", Version: "v1", Configured: true,
			MQTT: &mqttStatus, SHS: &shsStatus, SHSPhase: "на связи", SHSDot: "ok",
			Update: &upd, Checked: "5 с назад",
			Links: linksSummary{Total: 2, Enabled: 1, Delivered: 10},
		},
		"settings": settingsData{Title: "Настройки", Nav: "settings", Saved: true},
		"topics": topicsData{
			Title: "Топики", Nav: "topics", Total: 1,
			Groups: []topicGroup{{Prefix: "shellies/a", Topics: []topicRow{{
				Topic: "shellies/a/relay/0", Short: "relay/0", Payload: "on",
				Kind: "text", Count: 5, Ago: "3 с назад",
			}}}},
		},
		"links": linksData{
			Title: "Связки", Nav: "links", Total: 1,
			Groups: []linkGroup{{Device: "Реле", Prefix: "shellies/a", Links: []linkRow{{
				ID: 1, Enabled: true, Arrow: "шина ⇄ дом", Topic: "shellies/a/relay/0",
				CommandTopic: "shellies/a/relay/0/command", Addr: "758:4",
				Name: "Свет", Form: "byte", LastValue: "1", Paired: true,
			}}}},
		},
		"link_form": linkFormData{
			Title: "Связка", Nav: "links", New: true, Both: true,
			Elements: []elementOption{element}, Topics: []string{"shellies/a/relay/0"},
			ValuesText: "on = 1", ValuesOut: "toggle = toggle",
			Decode: "lamp", Kind: "command", QoS: 1,
		},
		"elements": elementsData{
			Title: "Элементы", Nav: "elements",
			Elements: []logic.Element{{ID: 758, SubID: 4, Name: "Свет", Type: "lamp", Area: "Прихожая"}},
		},
		"devices": devicesData{
			Title: "Устройства", Nav: "devices",
			Devices: []deviceRow{{Device: store.Device{ID: 1, Name: "Реле",
				TopicPrefix: "shellies/a", Model: "X", Template: "sample", Online: true},
				Links: 4, Enabled: 3}},
		},
		"device_form": deviceFormData{
			Title: "Устройство", Nav: "devices", DeviceID: 1,
			Templates: []devtmpl.Item{item}, Selected: item.Template, HasChoice: true,
			Name: "Реле", Prefix: "shellies/a",
			Prefixes: []prefixOption{{Prefix: "shellies/a", Topics: 5, Known: true}},
			Elements: []elementOption{element},
			Roles: []roleOption{{Role: item.Roles[0], Elements: []elementOption{element},
				Narrowed: true, Selected: "758:4"}},
		},
		"templates": templatesData{
			Title: "Профили", Nav: "templates", Dir: "/tmp/profiles",
			Templates: []devtmpl.Item{item}, Saved: "sample",
		},
		"template_edit": templateEditData{
			Title: "Профиль", Nav: "templates", Key: "sample",
			Body: "{}", Bundled: true,
		},
		"log": logData{
			Title: "Журнал", Nav: "log", Level: "info",
			Entries: []logRow{{Time: "12:00:00", Level: "error", Class: "bad",
				Text: "ошибка", Detail: "addr=1:2"}},
		},
		"preview": previewResult{OK: true, Value: "1", Hex: "01", Kind: "byte"},
		"updated": updateResult{OK: true},
	}

	pages := buildPages("/mqtt")
	for name := range pages {
		if _, ok := cases[name]; !ok {
			t.Errorf("для страницы %q нет данных в тесте — она не проверяется", name)
		}
	}

	for name, data := range cases {
		tmpl, ok := pages[name]
		if !ok {
			t.Errorf("страница %q не собрана", name)
			continue
		}
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
			t.Errorf("%s: отрисовка сорвалась: %v", name, err)
			continue
		}
		if buf.Len() == 0 {
			t.Errorf("%s: пустая страница", name)
		}
	}
}
