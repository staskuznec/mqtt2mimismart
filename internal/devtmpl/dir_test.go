package devtmpl

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func openDir(t *testing.T) *Dir {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "profiles"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return d
}

// При первом запуске поставляемые профили должны оказаться на диске: иначе
// человек открыл бы пустой раздел и не понял, с чего начинать.
func TestOpenSeedsBuiltin(t *testing.T) {
	d := openDir(t)

	items, err := d.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("каталог пуст — поставляемые профили не разложились")
	}
	for _, it := range items {
		if it.Error != "" {
			t.Errorf("профиль %s не разобрался: %s", it.Key, it.Error)
		}
		if !it.Bundled {
			t.Errorf("профиль %s не опознан как поставляемый", it.Key)
		}
	}
}

// Правки человека обновление шлюза не теряет — но и на месте не оставляет:
// правка уезжает в свой файл, поставляемый возвращается к эталону.
func TestSeedMovesUserChangesToCopy(t *testing.T) {
	d := openDir(t)

	path := filepath.Join(d.Path(), "shelly1.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	changed := strings.Replace(string(body), `"name": "Shelly 1`, `"name": "Моя правка`, 1)
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}

	// Повторное открытие — как перезапуск шлюза после обновления.
	again, err := Open(d.Path())
	if err != nil {
		t.Fatalf("повторное Open: %v", err)
	}

	got, err := again.Get("shelly1" + forkSuffix)
	if err != nil {
		t.Fatalf("копия с правкой не заведена: %v", err)
	}
	if !strings.HasPrefix(got.Name, "Моя правка") {
		t.Errorf("название копии = %q — правка потеряна", got.Name)
	}

	ref, err := again.Get("shelly1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if strings.HasPrefix(ref.Name, "Моя правка") {
		t.Error("поставляемый профиль остался с правкой — исправления пойдут мимо него")
	}

	// Поставляемый файл снова совпадает с эталоном, копия — своя, не его.
	items, _ := again.List()
	for _, it := range items {
		switch it.Key {
		case "shelly1":
			if it.Changed {
				t.Error("поставляемый профиль помечен изменённым после отката к эталону")
			}
		case "shelly1" + forkSuffix:
			if it.Bundled {
				t.Error("копия с правкой считается поставляемой")
			}
		}
	}
}

// Удалённый поставляемый возвращается: он едет в бинарнике, и удаление файла
// значит «взять заново», а не «убрать совсем».
func TestDeletedBundledComesBack(t *testing.T) {
	d := openDir(t)

	if err := d.Delete("shelly1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := d.Get("shelly1"); err == nil {
		t.Error("профиль читается сразу после удаления")
	}

	again, err := Open(d.Path())
	if err != nil {
		t.Fatalf("повторное Open: %v", err)
	}
	if _, err := again.Get("shelly1"); err != nil {
		t.Errorf("поставляемый профиль не вернулся: %v", err)
	}
}

// Свой файл, положенный в каталог руками, должен подхватываться.
func TestOwnProfileIsPickedUp(t *testing.T) {
	d := openDir(t)

	body := []byte(`{"name":"Своя модель","model":"X",
		"roles":[{"key":"a","title":"Канал","required":true}],
		"links":[{"name":"Состояние","direction":"in","role":"a",
			"topic":"{{prefix}}/relay/0","encode":"byte"}]}`)
	if err := os.WriteFile(filepath.Join(d.Path(), "my-own.json"), body, 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}

	got, err := d.Get("my-own")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Своя модель" {
		t.Errorf("название = %q", got.Name)
	}
	items, _ := d.List()
	for _, it := range items {
		if it.Key == "my-own" && it.Bundled {
			t.Error("свой профиль помечен поставляемым")
		}
	}
}

// Битый файл не должен исчезать из списка молча: иначе профиль пропадает из
// веба, и понять почему неоткуда.
func TestBrokenProfileIsListedWithError(t *testing.T) {
	d := openDir(t)

	if err := os.WriteFile(filepath.Join(d.Path(), "broken.json"), []byte("{не json"), 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}

	items, err := d.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, it := range items {
		if it.Key == "broken" {
			found = true
			if it.Error == "" {
				t.Error("битый профиль без описания ошибки")
			}
		}
	}
	if !found {
		t.Error("битый профиль пропал из списка")
	}
}

// Заведомо нерабочий профиль в каталог не попадает.
func TestSaveValidates(t *testing.T) {
	d := openDir(t)

	if _, err := d.Save("empty", []byte(`{"name":"без связок"}`)); err == nil {
		t.Error("профиль без связок сохранён")
	}
	if _, err := os.Stat(filepath.Join(d.Path(), "empty.json")); err == nil {
		t.Error("файл создан, хотя проверка не прошла")
	}
}

// Ключ становится именем файла и приходит из веба: без проверки «..» записал
// бы файл куда угодно.
func TestSaveRejectsPathEscape(t *testing.T) {
	d := openDir(t)

	for _, key := range []string{"../../etc/passwd", "a/b", `a\b`, "..", ""} {
		if _, err := d.Save(key, []byte(`{}`)); err == nil {
			t.Errorf("ключ %q принят", key)
		}
	}
}

// Исправление в поставляемом профиле должно доезжать до объекта. Узнать о нём
// оттуда больше неоткуда, а профиль с неверным путём в JSON молчит, не жалуясь:
// связка есть, ошибок нет, значений просто не появляется.
func TestSeedUpdatesUntouchedProfile(t *testing.T) {
	d := openDir(t)

	path := filepath.Join(d.Path(), "shelly1.json")
	bundled, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}

	// Кладём вид, в котором профиль якобы приехал прошлым обновлением, и
	// записываем его отпечаток: для раскладки это значит «клали мы, с тех пор
	// никто не трогал».
	old := strings.Replace(string(bundled), `"name": "Shelly 1`, `"name": "Прошлая версия`, 1)
	if old == string(bundled) {
		t.Fatal("подмена названия не сработала — профиль изменился")
	}
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	if err := d.writeState(map[string]string{"shelly1": checksum([]byte(old))}); err != nil {
		t.Fatalf("отпечатки: %v", err)
	}

	// Повторное открытие — как перезапуск шлюза после обновления.
	again, err := Open(d.Path())
	if err != nil {
		t.Fatalf("повторное Open: %v", err)
	}

	got, err := again.Raw("shelly1")
	if err != nil {
		t.Fatalf("Raw: %v", err)
	}
	if string(got) != string(bundled) {
		t.Error("профиль не обновился из поставки")
	}
	if !slices.Contains(again.Seeded().Updated, "shelly1") {
		t.Errorf("обновление не отмечено: %+v", again.Seeded())
	}
}

// Правка человека переживает обновление, но уже в своём файле: поставляемый
// профиль возвращается к эталону и обновляется дальше, а правка живёт копией.
// Так исправления перестают ходить мимо объекта, где однажды что-то подкрутили.
func TestSeedForksChangedProfile(t *testing.T) {
	d := openDir(t)

	path := filepath.Join(d.Path(), "shelly1.json")
	bundledBody, _ := os.ReadFile(path)
	mine := strings.Replace(string(bundledBody), `"name": "Shelly 1`, `"name": "Моя правка`, 1)
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	// Отпечаток остался от того вида, который клали мы, — значит файл трогали.
	if err := d.writeState(map[string]string{"shelly1": checksum(bundledBody)}); err != nil {
		t.Fatalf("отпечатки: %v", err)
	}

	again, err := Open(d.Path())
	if err != nil {
		t.Fatalf("повторное Open: %v", err)
	}

	got, _ := again.Raw("shelly1")
	if string(got) != string(bundledBody) {
		t.Error("поставляемый профиль не вернулся к эталону")
	}
	copied, err := again.Raw("shelly1" + forkSuffix)
	if err != nil {
		t.Fatalf("копия с правкой не заведена: %v", err)
	}
	if string(copied) != mine {
		t.Error("в копию легла не правка")
	}

	forks := again.Seeded().Forked
	if len(forks) != 1 || forks[0].Key != "shelly1" || forks[0].Copy != "shelly1"+forkSuffix {
		t.Errorf("откладывание копией не отмечено: %+v", forks)
	}
}

// Перезапуск за перезапуском не должен плодить копии: та же правка, уже
// отложенная, — это та же копия, а не новая.
func TestSeedDoesNotPileUpForks(t *testing.T) {
	d := openDir(t)

	path := filepath.Join(d.Path(), "shelly1.json")
	bundledBody, _ := os.ReadFile(path)
	mine := strings.Replace(string(bundledBody), `"name": "Shelly 1`, `"name": "Моя правка`, 1)

	for i := 0; i < 3; i++ {
		if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
			t.Fatalf("запись: %v", err)
		}
		if _, err := Open(d.Path()); err != nil {
			t.Fatalf("повторное Open: %v", err)
		}
	}

	items, err := d.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	copies := 0
	for _, it := range items {
		if strings.HasPrefix(it.Key, "shelly1"+forkSuffix) {
			copies++
		}
	}
	if copies != 1 {
		t.Errorf("копий с правкой %d, ожидалась одна", copies)
	}
}

// Разные правки — разные копии: вторая не затирает первую. Затереть чужую
// правку значит сделать ровно то, от чего копии и заводятся.
func TestSeedKeepsEveryFork(t *testing.T) {
	d := openDir(t)

	path := filepath.Join(d.Path(), "shelly1.json")
	bundledBody, _ := os.ReadFile(path)

	for _, name := range []string{"Первая правка", "Вторая правка"} {
		mine := strings.Replace(string(bundledBody), `"name": "Shelly 1`, `"name": "`+name, 1)
		if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
			t.Fatalf("запись: %v", err)
		}
		if _, err := Open(d.Path()); err != nil {
			t.Fatalf("повторное Open: %v", err)
		}
	}

	first, err := d.Raw("shelly1" + forkSuffix)
	if err != nil {
		t.Fatalf("первая копия пропала: %v", err)
	}
	if !strings.Contains(string(first), "Первая правка") {
		t.Error("первую копию затёрла вторая")
	}
	if _, err := d.Raw("shelly1" + forkSuffix + "-2"); err != nil {
		t.Fatalf("вторая копия не заведена: %v", err)
	}
}

// Файл отпечатков лежит рядом с профилями, но профилем не является: попав в
// список, он висел бы там строкой с ошибкой разбора.
func TestStateFileIsNotListed(t *testing.T) {
	d := openDir(t)

	if _, err := os.Stat(filepath.Join(d.Path(), stateFile)); err != nil {
		t.Fatalf("отпечатки не записаны: %v", err)
	}

	items, err := d.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, it := range items {
		if strings.HasPrefix(it.Key, ".") {
			t.Errorf("служебный файл показан профилем: %s", it.Key)
		}
	}
}

// Правка поставляемого профиля через веб тоже уезжает в копию: эталон должен
// оставаться эталоном, откуда бы правка ни пришла.
func TestSaveForksBundledProfile(t *testing.T) {
	d := openDir(t)

	body, err := d.Raw("shelly1")
	if err != nil {
		t.Fatalf("Raw: %v", err)
	}
	mine := strings.Replace(string(body), `"name": "Shelly 1`, `"name": "Моя правка`, 1)

	key, err := d.Save("shelly1", []byte(mine))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if key != "shelly1"+forkSuffix {
		t.Errorf("правка сохранена под ключом %q, ожидалась копия", key)
	}

	got, _ := d.Raw("shelly1")
	if string(got) != string(body) {
		t.Error("поставляемый профиль изменён правкой")
	}
	copied, _ := d.Raw(key)
	if string(copied) != mine {
		t.Error("в копию легла не правка")
	}
}

// Сохранение поставляемого профиля без изменений копии не заводит: человек
// открыл, посмотрел и нажал «сохранить» — файлов от этого прибавляться не
// должно.
func TestSaveWithoutChangesKeepsKey(t *testing.T) {
	d := openDir(t)

	body, _ := d.Raw("shelly1")
	key, err := d.Save("shelly1", body)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if key != "shelly1" {
		t.Errorf("ключ %q, ожидался прежний: профиль не менялся", key)
	}
}

// Свой профиль правится на месте: копировать нечего, эталона у него нет.
func TestSaveOwnProfileInPlace(t *testing.T) {
	d := openDir(t)

	body, _ := d.Raw("shelly1")
	mine := strings.Replace(string(body), `"name": "Shelly 1`, `"name": "Мой профиль`, 1)
	if _, err := d.Save("мой-профиль", []byte(mine)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	again := strings.Replace(mine, `"name": "Мой профиль`, `"name": "Мой профиль 2`, 1)
	key, err := d.Save("мой-профиль", []byte(again))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if key != "мой-профиль" {
		t.Errorf("ключ %q, ожидался прежний", key)
	}
	got, _ := d.Raw("мой-профиль")
	if string(got) != again {
		t.Error("правка своего профиля не сохранилась на месте")
	}
}

// Форма устройства должна знать, копией какого профиля работает устройство:
// иначе новые роли из поставки проходят мимо, и заметить это неоткуда.
func TestOrigin(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want string
	}{
		{"shelly1" + forkSuffix, "shelly1"},
		{"shelly1" + forkSuffix + "-2", "shelly1"},
		{"shelly1", ""}, // сам поставляемый копией не является
		{"мой-профиль" + forkSuffix, ""}, // копия своего профиля: эталона нет
		{forkSuffix, ""},                   // имя из одного суффикса
		{"shelly1" + forkSuffix + "x", ""}, // похожее имя, но не копия
	} {
		t.Run(tc.key, func(t *testing.T) {
			got, ok := Origin(tc.key)
			if tc.want == "" {
				if ok {
					t.Errorf("ключ %q сочтён копией профиля %q", tc.key, got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Errorf("Origin(%q) = %q, %v; ожидалось %q", tc.key, got, ok, tc.want)
			}
		})
	}
}

// Копия у поставляемого профиля одна. Иначе каждая правка эталона плодила бы
// файл, и через месяц в каталоге лежало бы десять «моих» shelly1, из которых
// не разобрать, какой рабочий.
func TestSaveRefusesSecondCopy(t *testing.T) {
	d := openDir(t)

	body, _ := d.Raw("shelly1")
	first := strings.Replace(string(body), `"name": "Shelly 1`, `"name": "Первая правка`, 1)
	key, err := d.Save("shelly1", []byte(first))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	second := strings.Replace(string(body), `"name": "Shelly 1`, `"name": "Вторая правка`, 1)
	_, err = d.Save("shelly1", []byte(second))

	var has ErrHasCopy
	if !errors.As(err, &has) {
		t.Fatalf("вторая правка эталона принята: %v", err)
	}
	if has.Copy != key {
		t.Errorf("отказ указывает на %q, ожидалась копия %q", has.Copy, key)
	}

	// Первая копия при этом цела: отказ не значит «затёрли».
	got, _ := d.Raw(key)
	if !strings.Contains(string(got), "Первая правка") {
		t.Error("копия изменилась при отказе")
	}

	// А сама копия правится сколько угодно — она уже своя.
	third := strings.Replace(string(got), "Первая правка", "Третья правка", 1)
	if _, err := d.Save(key, []byte(third)); err != nil {
		t.Fatalf("правка копии: %v", err)
	}
}
