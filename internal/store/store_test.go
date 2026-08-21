package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/staskuznec/mqtt2mimismart/internal/devtmpl"
)

// open создаёт временную базу для теста.
func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenAppliesMigrations(t *testing.T) {
	s := open(t)

	version, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	want, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("миграций не найдено вовсе")
	}
	if version != want[len(want)-1].version {
		t.Errorf("версия схемы = %d, ожидалась %d", version, want[len(want)-1].version)
	}
}

// Повторное открытие не должно накатывать миграции заново: иначе CREATE TABLE
// упадёт на второй попытке и демон не переживёт собственный перезапуск.
func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")

	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("первое открытие: %v", err)
	}
	if err := first.Set(context.Background(), "проверка", "значение"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}
	defer func() { _ = second.Close() }()

	// Данные должны пережить переоткрытие.
	value, ok, err := second.Get(context.Background(), "проверка")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || value != "значение" {
		t.Errorf("после переоткрытия получили (%q, %v), ожидали (%q, true)", value, ok, "значение")
	}
}

func TestOpenCreatesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "вложенный", "каталог", "gateway.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("файл базы не создан: %v", err)
	}
}

// В базе лежит ключ AES и пароль брокера, поэтому права строго 0600.
func TestOpenRestrictsFilePermissions(t *testing.T) {
	s := open(t)

	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != FileMode {
		t.Errorf("права на файл базы = %04o, ожидались %04o", got, FileMode)
	}
}

func TestGetMissingKey(t *testing.T) {
	s := open(t)

	value, ok, err := s.Get(context.Background(), "такого-ключа-нет")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Errorf("ключа нет, а Get вернул ok=true со значением %q", value)
	}
}

// Пустая строка и отсутствие ключа — разные вещи: в форме поле могли осознанно
// очистить, и это не то же самое, что «не настраивали».
func TestGetEmptyValueIsFound(t *testing.T) {
	s := open(t)

	if err := s.Set(context.Background(), "пусто", ""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value, ok, err := s.Get(context.Background(), "пусто")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || value != "" {
		t.Errorf("получили (%q, %v), ожидали (\"\", true)", value, ok)
	}
}

func TestSetOverwrites(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if err := s.Set(ctx, "ключ", "было"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set(ctx, "ключ", "стало"); err != nil {
		t.Fatalf("повторный Set: %v", err)
	}

	value, _, err := s.Get(ctx, "ключ")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "стало" {
		t.Errorf("значение = %q, ожидалось %q", value, "стало")
	}
}

func TestSetManyRejectsEmptyKey(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	err := s.SetMany(ctx, map[string]string{"хороший": "значение", "  ": "плохой"})
	if err == nil {
		t.Fatal("пустой ключ принят без ошибки")
	}

	// Транзакция должна откатиться целиком: частично сохранённая форма хуже,
	// чем несохранённая.
	if _, ok, _ := s.Get(ctx, "хороший"); ok {
		t.Error("часть настроек сохранилась, хотя транзакция должна была откатиться")
	}
}

func TestAll(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	want := map[string]string{"первый": "1", "второй": "2"}
	if err := s.SetMany(ctx, want); err != nil {
		t.Fatalf("SetMany: %v", err)
	}

	got, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("All()[%q] = %q, ожидалось %q", key, got[key], value)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	want := Config{
		MQTTAddr:     "192.168.20.10:1883",
		MQTTUser:     "gateway",
		MQTTPassword: "секрет",
		MQTTClientID: "gw-1",
		SHSAddr:      "127.0.0.1:9001",
		SHSKey:       "0123456789abcdef",
		SHSMac:       "aabbccddeeff",
	}
	if err := s.SaveConfig(ctx, want); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	got, err := s.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if got != want {
		t.Errorf("Config() = %+v, ожидалось %+v", got, want)
	}
	if !got.Ready() {
		t.Error("Ready() = false на полностью заполненной конфигурации")
	}
}

func TestConfigNotReadyWhenEmpty(t *testing.T) {
	s := open(t)

	cfg, err := s.Config(context.Background())
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.Ready() {
		t.Error("Ready() = true на пустой базе")
	}
}

// Длина ключа — требование AES. Клиент SHS с неверной длиной не стартует,
// поэтому ловим это при сохранении формы, а не в журнале через сутки.
func TestValidateKeyLength(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		ok   bool
	}{
		{"пустой не проверяется", "", true},
		{"16 байт", "0123456789abcdef", true},
		{"24 байта", "0123456789abcdef01234567", true},
		{"32 байта", "0123456789abcdef0123456789abcdef", true},
		{"15 байт", "0123456789abcde", false},
		{"17 байт", "0123456789abcdef0", false},
		// Длина меряется в байтах, а не в символах: восемь кириллических букв
		// занимают ровно 16 байт и ключом быть могут. Это не опечатка в тесте.
		{"8 кириллических символов — это 16 байт", "короткий", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Config{SHSKey: tc.key}.Validate()
			if tc.ok && err != nil {
				t.Errorf("Validate() = %v, ожидалась пустая ошибка", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatal("неверная длина ключа принята")
				}
				if !errors.Is(err, ErrKeyLength) {
					t.Errorf("Validate() = %v, ожидалась ErrKeyLength", err)
				}
			}
		})
	}
}

func TestValidateAddr(t *testing.T) {
	if err := (Config{MQTTAddr: "192.168.20.10"}).Validate(); err == nil {
		t.Error("адрес без порта принят")
	}
	if err := (Config{SHSAddr: "127.0.0.1"}).Validate(); err == nil {
		t.Error("адрес SHS без порта принят")
	}
}

// SaveConfig не должен записывать заведомо нерабочую конфигурацию.
func TestSaveConfigValidates(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	err := s.SaveConfig(ctx, Config{SHSAddr: "127.0.0.1:9001", SHSKey: "short"})
	if err == nil {
		t.Fatal("конфигурация с неверным ключом сохранена")
	}
	if _, ok, _ := s.Get(ctx, KeySHSAddr); ok {
		t.Error("настройки записаны, хотя проверка не прошла")
	}
}

func TestParseMigrationName(t *testing.T) {
	for _, tc := range []struct {
		file    string
		version int
		name    string
		wantErr bool
	}{
		{file: "0001_init.sql", version: 1, name: "init"},
		{file: "0042_add_links.sql", version: 42, name: "add_links"},
		{file: "init.sql", wantErr: true},
		{file: "abc_init.sql", wantErr: true},
		{file: "0000_init.sql", wantErr: true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			version, name, err := parseMigrationName(tc.file)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ошибки нет, разобрано в (%d, %q)", version, name)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMigrationName: %v", err)
			}
			if version != tc.version || name != tc.name {
				t.Errorf("получили (%d, %q), ожидали (%d, %q)", version, name, tc.version, tc.name)
			}
		})
	}
}

// Миграции обязаны идти строго по возрастанию и без повторов номеров.
func TestMigrationsAreOrderedAndUnique(t *testing.T) {
	all, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].version <= all[i-1].version {
			t.Errorf("миграции не по порядку: %d идёт после %d", all[i].version, all[i-1].version)
		}
	}
}

// Два одинаковых инвертора — самый частый случай на объекте, и название
// профиля их не различает: в списке связок и в журнале оба выглядят одинаково.
// Второму и следующим дописывается префикс топиков, он у каждого свой.
func TestApplyTemplateNamesDevicesApart(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	tmpl := devtmpl.Template{
		Key: "microart", Name: "МикроАрт МАП", Model: "map",
		Roles: []devtmpl.Role{{Key: "soc", Title: "Заряд"}},
		Links: []devtmpl.LinkSpec{{
			Name: "Заряд", Direction: "in", Role: "soc",
			Topic: "{{prefix}}/bat/0/C_100_remain", Encode: "text",
		}},
	}
	assign := map[string]devtmpl.Addr{"soc": {ID: 563, SubID: 114}}

	for _, prefix := range []string{"microart/inv1", "microart/inv2"} {
		if _, err := s.ApplyTemplate(ctx, Device{TopicPrefix: prefix}, tmpl, assign); err != nil {
			t.Fatalf("ApplyTemplate %s: %v", prefix, err)
		}
	}

	devices, err := s.Devices(ctx)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("устройств %d, ожидалось 2", len(devices))
	}
	if devices[0].Name == devices[1].Name {
		t.Errorf("оба устройства названы «%s» — их не различить", devices[0].Name)
	}

	// Имя устройства подставляется связкам при чтении: без него журнал не
	// говорит, с каким из инверторов работает движок.
	links, err := s.Links(ctx)
	if err != nil {
		t.Fatalf("Links: %v", err)
	}
	for _, l := range links {
		if l.Device == "" {
			t.Errorf("связке %q не подставлено имя устройства", l.Name)
		}
	}
}

// Правка поставляемого профиля уезжает в копию, и устройства, настроенные по
// нему, должны переехать на неё: иначе форма устройства откроется на эталоне,
// где правки нет, и следующее сохранение соберёт связки не те.
func TestRetargetDevices(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	tmpl := devtmpl.Template{
		Key: "shelly1", Name: "Shelly 1", Model: "SHSW-1",
		Roles: []devtmpl.Role{{Key: "ch0", Title: "Канал"}},
		Links: []devtmpl.LinkSpec{{
			Name: "Канал — состояние", Direction: "in", Role: "ch0",
			Topic: "{{prefix}}/relay/0", Encode: "byte",
		}},
	}
	assign := map[string]devtmpl.Addr{"ch0": {ID: 563, SubID: 57}}

	for _, prefix := range []string{"shellies/a", "shellies/b"} {
		if _, err := s.ApplyTemplate(ctx, Device{TopicPrefix: prefix}, tmpl, assign); err != nil {
			t.Fatalf("ApplyTemplate %s: %v", prefix, err)
		}
	}
	// Чужое устройство трогать нельзя: у него свой профиль.
	other := tmpl
	other.Key, other.Name = "tasmota-relay1", "Tasmota"
	if _, err := s.ApplyTemplate(ctx, Device{TopicPrefix: "tasmota/c"}, other, assign); err != nil {
		t.Fatalf("ApplyTemplate чужого: %v", err)
	}

	moved, err := s.RetargetDevices(ctx, "shelly1", "shelly1.local")
	if err != nil {
		t.Fatalf("RetargetDevices: %v", err)
	}
	if moved != 2 {
		t.Errorf("переведено %d устройств, ожидалось 2", moved)
	}

	devices, err := s.Devices(ctx)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	for _, d := range devices {
		want := "shelly1.local"
		if d.TopicPrefix == "tasmota/c" {
			want = "tasmota-relay1"
		}
		if d.Template != want {
			t.Errorf("%s: профиль %q, ожидался %q", d.TopicPrefix, d.Template, want)
		}
	}

	// Связки при переводе не трогаются: они уже заведены и работают.
	links, err := s.Links(ctx)
	if err != nil {
		t.Fatalf("Links: %v", err)
	}
	if len(links) != 3 {
		t.Errorf("связок %d, ожидалось 3: перевод устройства их не касается", len(links))
	}
}

// Смена профиля устройства — например, со своей копии обратно на поставляемый.
// Связки прежнего профиля должны уйти: их имена в новом могут не встретиться
// вовсе, и остались бы они висеть у устройства как заведённые руками, с
// топиками, которых в новом профиле нет.
func TestReapplyDropsLinksOfPreviousTemplate(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	dir, err := devtmpl.Open(t.TempDir())
	if err != nil {
		t.Fatalf("каталог профилей: %v", err)
	}
	s.SetTemplates(dir)

	profile := func(key, linkName string) devtmpl.Template {
		body := `{"name":"` + key + `","model":"SHSW-1","roles":[{"key":"ch0","title":"Канал"}],` +
			`"links":[{"name":"` + linkName + `","direction":"in","role":"ch0",` +
			`"topic":"{{prefix}}/relay/0","encode":"byte"}]}`
		if _, err := dir.Save(key, []byte(body)); err != nil {
			t.Fatalf("сохранение профиля %s: %v", key, err)
		}
		tmpl, err := dir.Get(key)
		if err != nil {
			t.Fatalf("чтение профиля %s: %v", key, err)
		}
		return tmpl
	}

	mine := profile("мой-профиль", "МОЙ Канал")
	stock := profile("поставляемый", "Канал 0 — состояние")
	assign := map[string]devtmpl.Addr{"ch0": {ID: 563, SubID: 57}}

	id, err := s.ApplyTemplate(ctx, Device{TopicPrefix: "shellies/a"}, mine, assign)
	if err != nil {
		t.Fatalf("ApplyTemplate: %v", err)
	}

	err = s.ReapplyTemplate(ctx, Device{ID: id, Name: "Тестовое", TopicPrefix: "shellies/a"},
		stock, assign)
	if err != nil {
		t.Fatalf("ReapplyTemplate: %v", err)
	}

	links, err := s.LinksByDevice(ctx, id)
	if err != nil {
		t.Fatalf("LinksByDevice: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("связок %d, ожидалась одна: связка прежнего профиля осталась сиротой: %+v", len(links), links)
	}
	if links[0].Name != "Канал 0 — состояние" {
		t.Errorf("осталась связка %q, ожидалась из нового профиля", links[0].Name)
	}
}

// Отказ на записи устройства не должен стоить ему связок. Префикс топиков
// уникален, и человек, вставивший чужой префикс, получал раньше ошибку SQL и
// пустое устройство: связки к этому моменту были уже удалены.
func TestReapplyKeepsLinksWhenDeviceUpdateFails(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	tmpl := devtmpl.Template{
		Key: "shelly1", Name: "Shelly 1", Model: "SHSW-1",
		Roles: []devtmpl.Role{{Key: "ch0", Title: "Канал"}},
		Links: []devtmpl.LinkSpec{{
			Name: "Канал 0 — состояние", Direction: "in", Role: "ch0",
			Topic: "{{prefix}}/relay/0", Encode: "byte",
		}},
	}
	assign := map[string]devtmpl.Addr{"ch0": {ID: 563, SubID: 57}}

	id, err := s.ApplyTemplate(ctx, Device{TopicPrefix: "shellies/a"}, tmpl, assign)
	if err != nil {
		t.Fatalf("ApplyTemplate: %v", err)
	}
	if _, err := s.ApplyTemplate(ctx, Device{TopicPrefix: "shellies/b"}, tmpl, assign); err != nil {
		t.Fatalf("ApplyTemplate соседа: %v", err)
	}

	// Префикс соседа: UPDATE упрётся в уникальный индекс.
	err = s.ReapplyTemplate(ctx, Device{ID: id, Name: "Тестовое", TopicPrefix: "shellies/b"},
		tmpl, assign)
	if err == nil {
		t.Fatal("чужой префикс принят, ожидалась ошибка")
	}

	links, err := s.LinksByDevice(ctx, id)
	if err != nil {
		t.Fatalf("LinksByDevice: %v", err)
	}
	if len(links) != 1 {
		t.Errorf("связок осталось %d, ожидалась одна: отказ стёр связки устройства", len(links))
	}
}

// Скрытые топики переживают перезапуск, поэтому лежат в базе. Ширина правила
// меняется на месте: скрыть отдельный топик, а потом всё устройство — обычный
// ход, и второй нажатой кнопке незачем плодить вторую запись.
func TestHiddenTopicsRoundTrip(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if err := s.HideTopic(ctx, "чужое/датчик/температура", false); err != nil {
		t.Fatalf("HideTopic: %v", err)
	}
	if err := s.HideTopic(ctx, "чужое/датчик/температура", true); err != nil {
		t.Fatalf("HideTopic повторно: %v", err)
	}

	list, err := s.HiddenTopics(ctx)
	if err != nil {
		t.Fatalf("HiddenTopics: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("скрытых %d, ожидалась одна запись: %v", len(list), list)
	}
	if !list[0].Tree {
		t.Error("повторное скрытие не расширило правило до мастер-топика")
	}
	if list[0].Since.IsZero() {
		t.Error("не записано, когда скрыли")
	}

	if err := s.ShowTopic(ctx, "чужое/датчик/температура"); err != nil {
		t.Fatalf("ShowTopic: %v", err)
	}
	list, err = s.HiddenTopics(ctx)
	if err != nil {
		t.Fatalf("HiddenTopics: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("после возврата осталось %d записей", len(list))
	}
}

// Пустой топик в скрытые не принимаем: такое правило накрыло бы шину целиком.
func TestHideTopicRejectsEmpty(t *testing.T) {
	if err := open(t).HideTopic(context.Background(), "   ", false); err == nil {
		t.Fatal("пустой топик принят, ожидалась ошибка")
	}
}
