package devtmpl

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dir — каталог с шаблонами на диске.
//
// Шаблоны лежат обычными файлами рядом с базой: их видно, их можно открыть
// редактором, положить туда новый или скопировать с другого объекта. Вшитыми в
// бинарник они были бы доступны только через веб, а это неудобно ровно тогда,
// когда неудобнее всего — при разборе, почему модель работает не так.
type Dir struct {
	path string
}

// Расширение файла шаблона.
const ext = ".json"

// Open открывает каталог, создавая его при необходимости, и раскладывает туда
// шаблоны, которые едут в бинарнике.
func Open(path string) (*Dir, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, fmt.Errorf("devtmpl: каталог шаблонов: %w", err)
	}

	d := &Dir{path: path}
	if err := d.seed(); err != nil {
		return nil, err
	}
	return d, nil
}

// Path возвращает путь к каталогу.
func (d *Dir) Path() string { return d.path }

// seed выкладывает встроенные шаблоны, которых ещё нет на диске.
//
// Только отсутствующие: правки в файлах — работа человека, и обновление шлюза
// не вправе её затирать. Новые модели при этом появляются сами.
func (d *Dir) seed() error {
	builtin, err := Builtin()
	if err != nil {
		return err
	}

	for _, t := range builtin {
		path := filepath.Join(d.path, t.Key+ext)
		if _, err := os.Stat(path); err == nil {
			continue // файл уже есть, не трогаем
		}
		body, err := templateFS.ReadFile("templates/" + t.Key + ext)
		if err != nil {
			return fmt.Errorf("devtmpl: чтение встроенного %s: %w", t.Key, err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return fmt.Errorf("devtmpl: запись %s: %w", path, err)
		}
	}
	return nil
}

// Item — шаблон вместе со сведениями о файле.
type Item struct {
	Template
	Bundled bool   // такой же ключ есть среди поставляемых
	Changed bool   // файл отличается от поставляемого
	Error   string // файл не разобрался
}

// List читает все шаблоны каталога.
//
// Файл, который не разобрался, не пропускается молча: он попадает в список с
// текстом ошибки. Иначе шаблон просто исчезал бы из веба, и понять почему
// было бы неоткуда.
func (d *Dir) List() ([]Item, error) {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return nil, fmt.Errorf("devtmpl: чтение каталога: %w", err)
	}

	bundled := make(map[string][]byte)
	if all, err := Builtin(); err == nil {
		for _, t := range all {
			if body, err := templateFS.ReadFile("templates/" + t.Key + ext); err == nil {
				bundled[t.Key] = body
			}
		}
	}

	out := make([]Item, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ext)

		body, err := os.ReadFile(filepath.Join(d.path, e.Name()))
		if err != nil {
			out = append(out, Item{Template: Template{Key: key, Name: key}, Error: err.Error()})
			continue
		}

		item := Item{}
		if ref, ok := bundled[key]; ok {
			item.Bundled = true
			item.Changed = string(ref) != string(body)
		}

		t, err := Parse(body)
		if err != nil {
			item.Template = Template{Key: key, Name: key}
			item.Error = err.Error()
			out = append(out, item)
			continue
		}
		t.Key = key
		item.Template = t
		out = append(out, item)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get читает один шаблон.
func (d *Dir) Get(key string) (Template, error) {
	body, err := d.Raw(key)
	if err != nil {
		return Template{}, err
	}
	t, err := Parse(body)
	if err != nil {
		return Template{}, err
	}
	t.Key = key
	return t, nil
}

// Raw отдаёт файл как есть — для редактора.
func (d *Dir) Raw(key string) ([]byte, error) {
	path, err := d.pathFor(key)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("devtmpl: шаблон %q не найден", key)
	}
	return body, nil
}

// Save проверяет и записывает шаблон.
func (d *Dir) Save(key string, body []byte) error {
	path, err := d.pathFor(key)
	if err != nil {
		return err
	}
	// Проверка до записи: шаблон применяют один раз и потом доверяют ему.
	if _, err := Parse(body); err != nil {
		return err
	}

	// Пишем рядом и переименовываем: половина файла на диске означала бы
	// шаблон, который не читается, вместо прежнего рабочего.
	tmp := path + ".new"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("devtmpl: запись %s: %w", path, err)
	}
	return os.Rename(tmp, path)
}

// Delete удаляет шаблон. Поставляемый вернётся при следующем запуске: он едет
// в бинарнике, и удаление файла означает «взять заново», а не «убрать совсем».
func (d *Dir) Delete(key string) error {
	path, err := d.pathFor(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("devtmpl: удаление %s: %w", path, err)
	}
	return nil
}

// pathFor собирает путь к файлу, не выпуская за пределы каталога.
func (d *Dir) pathFor(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("devtmpl: пустой ключ шаблона")
	}
	// Ключ приходит из веба и становится именем файла: без этой проверки
	// "../../etc/passwd" записался бы куда угодно.
	if strings.ContainsAny(key, `/\`) || strings.Contains(key, "..") {
		return "", fmt.Errorf("devtmpl: недопустимый ключ %q: только буквы, цифры, дефис", key)
	}
	return filepath.Join(d.path, key+ext), nil
}
