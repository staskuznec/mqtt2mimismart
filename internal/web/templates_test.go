package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
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
