package devtmpl

import (
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

// Правки человека обновление шлюза затирать не вправе.
func TestSeedKeepsUserChanges(t *testing.T) {
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
	got, err := again.Get("shelly1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.HasPrefix(got.Name, "Моя правка") {
		t.Errorf("название = %q — правка затёрта при перезапуске", got.Name)
	}

	// И такой профиль помечается как изменённый.
	items, _ := again.List()
	for _, it := range items {
		if it.Key == "shelly1" && !it.Changed {
			t.Error("изменённый профиль не помечен")
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

	if err := d.Save("empty", []byte(`{"name":"без связок"}`)); err == nil {
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
		if err := d.Save(key, []byte(`{}`)); err == nil {
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

// Правка человека переживает и то обновление, в котором поставляемый профиль
// изменился: потерять чужую работу хуже, чем не довезти исправление.
func TestSeedKeepsChangedProfileOnUpdate(t *testing.T) {
	d := openDir(t)

	path := filepath.Join(d.Path(), "shelly1.json")
	bundled, _ := os.ReadFile(path)
	mine := strings.Replace(string(bundled), `"name": "Shelly 1`, `"name": "Моя правка`, 1)
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	// Отпечаток остался от того вида, который клали мы, — значит файл трогали.
	if err := d.writeState(map[string]string{"shelly1": checksum(bundled)}); err != nil {
		t.Fatalf("отпечатки: %v", err)
	}

	again, err := Open(d.Path())
	if err != nil {
		t.Fatalf("повторное Open: %v", err)
	}
	got, _ := again.Raw("shelly1")
	if string(got) != mine {
		t.Error("правка затёрта обновлением")
	}
	if !slices.Contains(again.Seeded().Kept, "shelly1") {
		t.Errorf("профиль не отмечен как оставленный: %+v", again.Seeded())
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

// Если исправление вышло, а профиль правлен вручную, об этом надо сказать: сам
// человек об упущенном исправлении не узнает никак. Просто правленый профиль
// поводом для предупреждения не является — иначе оно висело бы при каждом
// запуске у всех, кто хоть раз что-то настроил под себя.
func TestSeedReportsMissedFix(t *testing.T) {
	d := openDir(t)

	path := filepath.Join(d.Path(), "shelly1.json")
	bundled, _ := os.ReadFile(path)
	mine := strings.Replace(string(bundled), `"name": "Shelly 1`, `"name": "Моя правка`, 1)
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}

	// Отпечаток остался от прошлой поставки: она отличается и от нынешней, и
	// от правки — значит с тех пор профиль в поставке менялся.
	if err := d.writeState(map[string]string{
		"shelly1": checksum([]byte("вид из прошлой поставки")),
	}); err != nil {
		t.Fatalf("отпечатки: %v", err)
	}

	again, err := Open(d.Path())
	if err != nil {
		t.Fatalf("повторное Open: %v", err)
	}
	if got, _ := again.Raw("shelly1"); string(got) != mine {
		t.Error("правка затёрта")
	}
	if !slices.Contains(again.Seeded().Stale, "shelly1") {
		t.Errorf("упущенное исправление не отмечено: %+v", again.Seeded())
	}
}

// А вот у профиля, который просто правлен под себя, ничего не упущено: в
// поставке он с прошлого раза не менялся.
func TestEditedProfileWithoutNewVersionIsNotStale(t *testing.T) {
	d := openDir(t)

	path := filepath.Join(d.Path(), "shelly1.json")
	bundled, _ := os.ReadFile(path)
	mine := strings.Replace(string(bundled), `"name": "Shelly 1`, `"name": "Моя правка`, 1)
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatalf("запись: %v", err)
	}
	if err := d.writeState(map[string]string{"shelly1": checksum(bundled)}); err != nil {
		t.Fatalf("отпечатки: %v", err)
	}

	again, err := Open(d.Path())
	if err != nil {
		t.Fatalf("повторное Open: %v", err)
	}
	if slices.Contains(again.Seeded().Stale, "shelly1") {
		t.Error("правленый профиль отмечен как упустивший исправление, хотя в поставке ничего не менялось")
	}
}
