package web

import (
	"bytes"
	"io"
	"io/fs"
	"log/slog"
	"net/http/httptest"
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
// Атрибут form="…" у поля означает «это поле принадлежит форме с таким id».
// Если формы с таким id нет, поле не принадлежит никакой — и в отправку не
// попадает вовсе. Ошибка тихая: страница выглядит целой, а значение с неё не
// уходит. Ровно так проба в редакторе связок отвечала «сообщение пустое» на
// любой введённый пример.
func TestFormAttributesPointAtExistingForm(t *testing.T) {
	attr := regexp.MustCompile(`\sform="([^"]+)"`)

	err := fs.WalkDir(templateFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		body, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}

		for _, m := range attr.FindAllStringSubmatch(string(body), -1) {
			if !strings.Contains(string(body), `id="`+m[1]+`"`) {
				t.Errorf("%s: поле привязано к форме %q, которой в шаблоне нет — "+
					"значение такого поля никуда не отправляется", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход шаблонов: %v", err)
	}
}

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
			Title: "Топики", Nav: "topics", Total: 3,
			Groups: []topicGroup{
				{Prefix: "shellies/a", Topics: []topicRow{
					{Topic: "shellies/a/relay/0", Short: "relay/0", Payload: "on",
						Kind: "text", Count: 5, Ago: "3 с назад"},
					{Topic: "shellies/a/relay/0/power", Short: "relay/0/power",
						Payload: "41.2", Kind: "number", Count: 9, Ago: "1 с назад"},
				}},
				{Prefix: "shellies/b", Topics: []topicRow{
					{Topic: "shellies/b/temperature", Short: "temperature",
						Payload: "46.06", Kind: "number", Count: 2, Ago: "8 с назад"},
				}},
			},
			Hidden: []hiddenRow{
				{Topic: "tele/чужой-датчик", Tree: true, Since: "2 ч назад"},
				{Topic: "zigbee2mqtt/bridge/log", Since: "вчера"},
			},
			Error: "Топик shellies/a/relay/0 не скрыть: на нём работают связки — Свет.",
		},
		"links": linksData{
			Title: "Связки", Nav: "links", Total: 1,
			Groups: []linkGroup{{Device: "Реле", Prefix: "shellies/a", Links: []linkRow{
				{ID: 1, Enabled: true, Arrow: "шина ⇄ дом", Topic: "shellies/a/relay/0",
					CommandTopic: "shellies/a/relay/0/command", Addr: "758:4",
					Name: "Свет", Form: "byte", LastValue: "1", Paired: true},
				{ID: 2, Enabled: false, Arrow: "шина → дом", Topic: "shellies/a/relay/0/power",
					Addr: "563:120", Form: "text", LastValue: "41 Вт",
					Errors: 2, LastError: "значение не число"},
			}}},
		},
		"link_form": linkFormData{
			Title: "Связка", Nav: "links", New: true, Both: true,
			Elements: []elementOption{element}, Topics: []string{"shellies/a/relay/0"},
			ValuesText: "on = 1", ValuesOut: "toggle = toggle",
			Decode: "lamp", Kind: "command", QoS: 1,
		},
		"elements_new": elementsNewData{
			Title: "Завести элементы", Nav: "elements",
			Templates: []devtmpl.Item{item}, Selected: item.Template, HasChoice: true,
			Modules: []uint16{563}, Module: 563, FromSub: 115,
			Area: "Инвертор1", Areas: []string{"Инвертор1"}, Prefix: "microart/inv1",
			XML: "        <area name=\"Инвертор1\">\n        </area>\n",
			Items: []genRow{{Role: "ch0", Title: "Канал", Addr: "563:115",
				Name: "Канал", Kind: "text", Topic: "microart/inv1/relay/0"}},
			Link: "devices/new?template=sample",
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
		"device_created": deviceCreatedData{
			Title: "Элементы заведены", Nav: "devices", Device: "Инвертор 1",
			Area: "Инвертор1", XML: "        <area name=\"Инвертор1\">\n        </area>\n",
			Created: []createdRow{{Title: "Заряд АКБ", Name: "Инвертор 1 Заряд АКБ",
				Addr: "563:115", Kind: "sensor"}},
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
			continue
		}
		checkTags(t, name, buf.String())
	}
}

// Топики одного инвертора МикроАрт — полторы сотни строк, а его же раздел
// целиком приезжает одной строкой на килобайт. Без раскладушки и обрезки
// страница превращается в портянку, в которой не видно ни соседних устройств,
// ни кнопки «Связать»: она уезжает за правый край таблицы.
func TestTopicsPageFoldsLongContent(t *testing.T) {
	rows := make([]topicRow, 0, bigGroup+1)
	for i := 0; i <= bigGroup; i++ {
		rows = append(rows, topicRow{Topic: "microart/inv1/map/_UNET",
			Short: "map/_UNET", Payload: "228", Kind: "number", Count: 3, Ago: "3 с назад"})
	}
	data := topicsData{Title: "Топики", Nav: "topics", Total: len(rows) + 1, Groups: []topicGroup{
		{Prefix: "microart/inv1", Topics: rows, Collapsed: true},
		{Prefix: "shellies/a", Topics: rows[:1]},
	}}

	var buf bytes.Buffer
	if err := buildPages("/mqtt")["topics"].ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("отрисовка сорвалась: %v", err)
	}

	html := buf.String()
	for _, want := range []string{
		// Большая группа приезжает свёрнутой, маленькая — открытой.
		`<details class="group" data-prefix="microart/inv1">`,
		`<details class="group" data-prefix="shellies/a" open>`,
		// Значение обрезается стилем, а не сервером: полное открывается щелчком.
		`<span class="clip" data-field="payload">228</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("в странице нет фрагмента %s", want)
		}
	}
	if open, closed := strings.Count(html, "<details"), strings.Count(html, "</details>"); open != closed {
		t.Errorf("тег <details> открыт %d раз, закрыт %d", open, closed)
	}
}

// Элементов умного дома на объекте четыре сотни, и в форме устройства список
// повторяется по одному на каждую роль профиля. Без поиска нужный элемент там
// не находится вовсе: имена отличаются одним словом или номером. Поиск живёт
// в layout и цепляется к любому длинному списку сам — своего в страницах быть
// не должно, иначе над одним списком окажется два поля.
func TestLongSelectsGetSearch(t *testing.T) {
	layout, err := templateFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("чтение layout: %v", err)
	}
	for _, want := range []string{`box.className = "pick"`, `querySelectorAll("select")`} {
		if !strings.Contains(string(layout), want) {
			t.Errorf("в layout нет поиска по спискам: %s", want)
		}
	}

	err = fs.WalkDir(templateFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") ||
			strings.HasSuffix(path, "layout.html") {
			return err
		}
		body, err := templateFS.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "filterElements") {
			t.Errorf("%s: свой поиск по списку — он уже есть в layout", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход шаблонов: %v", err)
	}
}

// checkTags ловит теги, оказавшиеся внутри цикла.
//
// Закрывающий {{end}} после </tbody> выглядит безобидно: разбор проходит,
// одна строка рисуется верно. А на нескольких таблица закрывается после
// первой, и остальные строки браузер выбрасывает наружу простым текстом.
// Ровно так сломались «Топики».
func checkTags(t *testing.T, page, html string) {
	t.Helper()

	for _, tag := range []string{"table", "tbody", "thead", "form", "div", "select"} {
		open := strings.Count(html, "<"+tag+" ") + strings.Count(html, "<"+tag+">")
		closed := strings.Count(html, "</"+tag+">")
		if open != closed {
			t.Errorf("%s: тег <%s> открыт %d раз, закрыт %d — вёрстка развалится",
				page, tag, open, closed)
		}
	}

	// Строк в таблице должно быть столько же, сколько закрывающих тегов:
	// незакрытая строка так же ломает разметку.
	if rows := strings.Count(html, "<tr"); rows != strings.Count(html, "</tr>") {
		t.Errorf("%s: строк таблицы %d, закрыто %d", page, rows, strings.Count(html, "</tr>"))
	}
}

// Роли, подставленные ссылкой со страницы «Завести элементы».
//
// Без этого человеку пришлось бы разложить полсотни свежих адресов по ролям
// руками — ровно ту работу, ради избавления от которой генератор и делался.
// Сохранённое назначение при этом сильнее ссылки: устройство уже настроено, и
// переписать его переходом по адресу было бы неожиданно.
func TestAssignFromQuery(t *testing.T) {
	tmpl := devtmpl.Template{
		Key: "sample",
		Roles: []devtmpl.Role{
			{Key: "ch0", Title: "Канал"},
			{Key: "temp", Title: "Температура"},
			{Key: "bad", Title: "Мусор"},
		},
	}
	r := httptest.NewRequest("GET",
		"/devices/new?template=sample&role_ch0=563:115&role_temp=563:116&role_bad=нет", nil)

	got := assignFromQuery(r, tmpl, map[string]devtmpl.Addr{"ch0": {ID: 758, SubID: 4}})

	if got["ch0"] != (devtmpl.Addr{ID: 758, SubID: 4}) {
		t.Errorf("сохранённое назначение переписано ссылкой: %v", got["ch0"])
	}
	if got["temp"] != (devtmpl.Addr{ID: 563, SubID: 116}) {
		t.Errorf("роль temp = %v, ожидалось 563:116", got["temp"])
	}
	if _, ok := got["bad"]; ok {
		t.Error("мусор в адресе принят за назначение")
	}
}

// Страницы шлюза живые: список элементов меняется, когда правят logic.xml.
// Ответ без запрета кэширования браузер и прокси перед панелью умного дома
// вправе показать повторно — и человек ищет в форме устройства только что
// заведённый элемент, которого там «нет».
func TestPagesAreNotCached(t *testing.T) {
	s := &server{pages: buildPages(""), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()

	s.render(rec, "elements", elementsData{Title: "Элементы", Nav: "elements"})

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, ожидалось no-store", got)
	}
}
