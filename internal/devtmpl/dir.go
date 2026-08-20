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
	Forked  []Fork   // правленые отложены копией, поставляемый обновлён
}

// Fork — правка поставляемого профиля, отложенная в отдельный файл.
//
// Раньше правленый файл оставался на месте, и исправления из поставки шли
// мимо него: на объекте профиль годами оставался таким, каким его однажды
// подкрутили, а о новых ролях узнать было неоткуда. Теперь правка уезжает в
// свой файл, а поставляемый возвращается к эталону и обновляется дальше.
type Fork struct {
	Key  string // поставляемый ключ, файл которого правили
	Copy string // куда легли правки
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
			// было. Отличить одно от другого нельзя, и потерять чужую правку
			// нельзя тем более. Поэтому правка уезжает в отдельный файл, а
			// поставляемый возвращается к эталону: копия остаётся рабочей,
			// а исправления перестают ходить мимо профиля.
			copyKey, err := d.fork(t.Key, current)
			if err != nil {
				return err
			}
			if err := write(path, body); err != nil {
				return err
			}
			next[t.Key] = sum
			d.seeded.Forked = append(d.seeded.Forked, Fork{Key: t.Key, Copy: copyKey})
		}
	}

	return d.writeState(next)
}

// fork откладывает правленый профиль в отдельный файл и возвращает его ключ.
//
// Имя — «<ключ>.local», рядом с исходным: в каталоге сразу видно, от чего
// копия. Занятое имя не перезаписываем, а берём следующее: под ним может
// лежать прошлая правка, и затереть её значит сделать ровно то, от чего мы
// уходим. Копия, совпадающая с правкой байт в байт, считается своей — иначе
// каждый перезапуск плодил бы новый файл.
func (d *Dir) fork(key string, body []byte) (string, error) {
	for i := 1; i <= forkLimit; i++ {
		name := key + forkSuffix
		if i > 1 {
			name = fmt.Sprintf("%s%s-%d", key, forkSuffix, i)
		}

		path := filepath.Join(d.path, name+ext)
		current, err := os.ReadFile(path)
		switch {
		case os.IsNotExist(err):
			if err := write(path, body); err != nil {
				return "", err
			}
			return name, nil
		case err != nil:
			return "", fmt.Errorf("devtmpl: чтение %s: %w", path, err)
		case string(current) == string(body):
			return name, nil // такая копия уже лежит
		}
	}
	return "", fmt.Errorf("devtmpl: у профиля %q уже %d копий с правками; "+
		"разберите их в каталоге профилей", key, forkLimit)
}

// Копии правленых профилей.
const (
	forkSuffix = ".local"
	forkLimit  = 20
)

// Origin сообщает, копией какого поставляемого профиля является ключ.
//
// Нужно форме устройства: работая по своей копии, человек не видит, что в
// поставляемом профиле с тех пор появились роли, — а именно так и теряются
// новые показания. Ссылка на исходный профиль стоит ровно там, где о нём
// вспоминают.
func Origin(key string) (string, bool) {
	cut := strings.Index(key, forkSuffix)
	if cut <= 0 {
		return "", false
	}
	// Хвост после «.local» — только номер копии: «.local», «.local-2».
	tail := key[cut+len(forkSuffix):]
	if tail != "" && !strings.HasPrefix(tail, "-") {
		return "", false
	}

	base := key[:cut]
	if _, ok := bundled(base); !ok {
		return "", false
	}
	return base, true
}

// bundled отдаёт поставляемый профиль как он едет в бинарнике.
func bundled(key string) ([]byte, bool) {
	body, err := templateFS.ReadFile(key + ext)
	if err != nil {
		return nil, false
	}
	return body, true
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

// ErrHasCopy — у поставляемого профиля уже есть копия с правками.
//
// Копия у профиля одна: иначе каждая правка эталона плодила бы файл, и через
// месяц в каталоге лежало бы десять «моих» shelly25-relay, из которых поди
// разбери, какой рабочий. Поэтому вторая правка не сохраняется молча ни в
// новый файл, ни поверх копии — вызывающий отправляет человека править копию.
type ErrHasCopy struct {
	Key  string // поставляемый профиль
	Copy string // его копия с правками
}

func (e ErrHasCopy) Error() string {
	return fmt.Sprintf("у профиля %q уже есть ваша копия %q — правьте её: "+
		"поставляемый остаётся эталонным и обновляется со шлюзом", e.Key, e.Copy)
}

// Save сохраняет профиль и возвращает ключ, под которым он лёг.
//
// Ключ возвращается не для порядка: правка поставляемого профиля уезжает в
// копию, и вызывающему надо знать, куда именно, — устройства переводятся на
// неё, и открыть после сохранения нужно тоже её. Поставляемый файл остаётся
// эталоном и продолжает обновляться вместе со шлюзом.
func (d *Dir) Save(key string, body []byte) (string, error) {
	path, err := d.pathFor(key)
	if err != nil {
		return "", err
	}
	// Проверка до записи: шаблон применяют один раз и потом доверяют ему.
	if _, err := Parse(body); err != nil {
		return "", err
	}

	if ref, ok := bundled(key); ok && string(ref) != string(body) {
		if copyKey, ok := d.copyOf(key); ok {
			return "", ErrHasCopy{Key: key, Copy: copyKey}
		}
		return d.fork(key, body)
	}
	return key, write(path, body)
}

// copyOf ищет копию поставляемого профиля.
func (d *Dir) copyOf(key string) (string, bool) {
	name := key + forkSuffix
	if _, err := os.Stat(filepath.Join(d.path, name+ext)); err == nil {
		return name, true
	}
	return "", false
}

// HasCopy сообщает, есть ли у поставляемого профиля копия с правками. Нужно
// редактору: открывая эталон, человек должен видеть, что рабочая версия —
// рядом, а не узнавать об этом отказом при сохранении.
func (d *Dir) HasCopy(key string) (string, bool) { return d.copyOf(key) }

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
