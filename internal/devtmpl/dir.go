package devtmpl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	path   string
	seeded SeedResult
}

// Расширение файла шаблона.
const ext = ".json"

// stateFile — отпечатки того, что раскладка клала сама.
//
// Лежит рядом с шаблонами и в список не попадает: имена, начинающиеся с
// точки, [Dir.List] пропускает.
const stateFile = ".bundled.json"

// SeedResult — что раскладка сделала с шаблонами при запуске.
type SeedResult struct {
	Added   []string // появились впервые
	Updated []string // подтянуто исправление из поставки
	Kept    []string // оставлены как есть: правлены руками

	// Stale — те из Kept, мимо которых прошло настоящее исправление: в
	// поставке профиль с прошлого раза изменился, а файл правлен вручную.
	// Только об этом и стоит предупреждать. Просто правленый профиль —
	// обычное дело, и говорить о нём при каждом запуске незачем.
	Stale []string
}

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

// Seeded сообщает, что раскладка сделала с шаблонами при запуске.
func (d *Dir) Seeded() SeedResult { return d.seeded }

// seed выкладывает поставляемые шаблоны и подтягивает их исправления.
//
// Правки в файлах — работа человека, и обновление шлюза не вправе её затирать.
// Но и оставить исправленный шаблон лежать на объекте в прежнем виде нельзя:
// узнать об исправлении оттуда неоткуда, а профиль с неверным путём молчит, не
// жалуясь.
//
// Поэтому рядом хранится отпечаток того, что раскладка клала сама. Файл,
// совпадающий с отпечатком, никто не трогал — его можно обновить молча. Всё
// остальное остаётся как есть и помечается в вебе как изменённое.
func (d *Dir) seed() error {
	builtin, err := Builtin()
	if err != nil {
		return err
	}

	known := d.readState()
	next := make(map[string]string, len(builtin))

	for _, t := range builtin {
		body, err := templateFS.ReadFile(t.Key + ext)
		if err != nil {
			return fmt.Errorf("devtmpl: чтение поставляемого %s: %w", t.Key, err)
		}
		sum := checksum(body)
		path := filepath.Join(d.path, t.Key+ext)

		current, err := os.ReadFile(path)
		switch {
		case os.IsNotExist(err):
			if err := write(path, body); err != nil {
				return err
			}
			next[t.Key] = sum
			d.seeded.Added = append(d.seeded.Added, t.Key)

		case err != nil:
			return fmt.Errorf("devtmpl: чтение %s: %w", path, err)

		case checksum(current) == sum:
			// Уже то же самое. Отпечаток при этом ставим: так шлюз, впервые
			// поднявшийся с этой раскладкой, признаёт нетронутые файлы
			// своими и в дальнейшем обновляет их сам.
			next[t.Key] = sum

		case known[t.Key] != "" && known[t.Key] == checksum(current):
			// Наша копия, к которой никто не притрагивался.
			if err := write(path, body); err != nil {
				return err
			}
			next[t.Key] = sum
			d.seeded.Updated = append(d.seeded.Updated, t.Key)

		default:
			// Правлен руками — или пришёл из версии, когда отпечатков ещё не
			// было. Отличить одно от другого нельзя, поэтому не трогаем:
			// потерять чужую правку хуже, чем не довезти исправление.
			// Прежний отпечаток сохраняем, иначе следующее обновление сочло
			// бы этот файл нашим и затёрло.
			d.seeded.Kept = append(d.seeded.Kept, t.Key)
			if prev := known[t.Key]; prev != "" {
				next[t.Key] = prev
				if prev != sum {
					// С прошлого раза поставляемый профиль изменился, а этот
					// файл правлен: исправление прошло мимо него.
					d.seeded.Stale = append(d.seeded.Stale, t.Key)
				}
			}
		}
	}

	return d.writeState(next)
}

// checksum — отпечаток содержимого файла.
func checksum(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// write кладёт файл через временный: половина файла на диске означала бы
// шаблон, который не читается, вместо прежнего рабочего.
func write(path string, body []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("devtmpl: запись %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("devtmpl: замена %s: %w", path, err)
	}
	return nil
}

// readState читает отпечатки. Битый файл — не повод не запускаться: худшее,
// что случится, это неподтянутое исправление.
func (d *Dir) readState() map[string]string {
	body, err := os.ReadFile(filepath.Join(d.path, stateFile))
	if err != nil {
		return nil
	}
	var state map[string]string
	if err := json.Unmarshal(body, &state); err != nil {
		return nil
	}
	return state
}

func (d *Dir) writeState(state map[string]string) error {
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("devtmpl: отпечатки: %w", err)
	}
	return write(filepath.Join(d.path, stateFile), append(body, '\n'))
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
			if body, err := templateFS.ReadFile(t.Key + ext); err == nil {
				bundled[t.Key] = body
			}
		}
	}

	out := make([]Item, 0, len(entries))
	for _, e := range entries {
		// Точка в начале — служебное: отпечатки раскладки, следы
		// редакторов. Профилем такое показывать незачем.
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) ||
			strings.HasPrefix(e.Name(), ".") {
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

	return write(path, body)
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
