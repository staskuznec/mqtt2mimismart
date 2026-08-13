package store

import (
	"context"
	"errors"
	"testing"

	"github.com/staskuznec/mqtt2mimismart/internal/devtmpl"
	"github.com/staskuznec/mqtt2mimismart/internal/link"
)

// sampleIn — типовая связка «состояние реле в лампу».
func sampleIn() link.Link {
	precision := 1
	return link.Link{
		Enabled:   true,
		Name:      "Прихожая, канал 0",
		Direction: link.In,
		Topic:     "shellies/shelly25-A1/relay/0",
		Extract:   link.ExtractRaw,
		Values:    map[string]string{"on": "1", "off": "0", "overpower": "1"},
		Encode:    link.EncodeByte,
		Unit:      " Вт",
		Precision: &precision,
		TargetID:  563, TargetSubID: 57,
		OnlyChanged: true,
	}
}

func TestCreateAndReadLink(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	want := sampleIn()
	id, err := s.CreateLink(ctx, want)
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	got, err := s.Link(ctx, id)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}

	want.ID = id
	if got.Topic != want.Topic || got.Encode != want.Encode ||
		got.TargetID != want.TargetID || got.TargetSubID != want.TargetSubID {
		t.Errorf("связка вернулась изменённой:\nполучено %+v\nожидалось %+v", got, want)
	}
	if len(got.Values) != 3 || got.Values["on"] != "1" || got.Values["overpower"] != "1" {
		t.Errorf("таблица значений = %v", got.Values)
	}
	if got.Precision == nil || *got.Precision != 1 {
		t.Errorf("точность = %v, ожидалась 1", got.Precision)
	}
	if !got.OnlyChanged || !got.Enabled {
		t.Errorf("флаги потерялись: OnlyChanged=%v Enabled=%v", got.OnlyChanged, got.Enabled)
	}
}

// Отсутствие точности и отсутствие нуля — разные вещи: nil означает «без
// округления», и после обхода через базу это должно сохраниться.
func TestPrecisionNilSurvivesRoundTrip(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	l := sampleIn()
	l.Precision = nil
	id, err := s.CreateLink(ctx, l)
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	got, err := s.Link(ctx, id)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if got.Precision != nil {
		t.Errorf("точность = %v, ожидалось nil", *got.Precision)
	}

	zero := 0
	l.Precision = &zero
	id, err = s.CreateLink(ctx, l)
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	got, err = s.Link(ctx, id)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if got.Precision == nil || *got.Precision != 0 {
		t.Errorf("точность = %v, ожидался ноль", got.Precision)
	}
}

// Заведомо нерабочая связка не должна попадать в базу: движок спотыкался бы
// о неё на каждом сообщении, а в вебе она выглядела бы настроенной.
func TestCreateLinkValidates(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	bad := sampleIn()
	bad.Encode = ""
	if _, err := s.CreateLink(ctx, bad); err == nil {
		t.Error("связка без формы значения сохранена")
	}

	retained := link.Link{
		Enabled: true, Direction: link.Out,
		Topic: "shellies/a/relay/0/command", Decode: link.DecodeLamp, Retain: true,
	}
	if _, err := s.CreateLink(ctx, retained); err == nil {
		t.Error("команда с retain сохранена")
	}

	links, err := s.Links(ctx)
	if err != nil {
		t.Fatalf("Links: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("в базе %d связок, ожидалось 0", len(links))
	}
}

func TestUpdateLink(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	id, err := s.CreateLink(ctx, sampleIn())
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	l, _ := s.Link(ctx, id)
	l.Topic = "shellies/shelly25-B2/relay/1"
	l.TargetSubID = 58
	l.Values = map[string]string{"on": "1"}
	if err := s.UpdateLink(ctx, l); err != nil {
		t.Fatalf("UpdateLink: %v", err)
	}

	got, _ := s.Link(ctx, id)
	if got.Topic != "shellies/shelly25-B2/relay/1" || got.TargetSubID != 58 {
		t.Errorf("связка не обновилась: %+v", got)
	}
	if len(got.Values) != 1 {
		t.Errorf("таблица значений = %v, ожидалась одна пара", got.Values)
	}
}

func TestUpdateMissingLink(t *testing.T) {
	s := open(t)

	l := sampleIn()
	l.ID = 999
	err := s.UpdateLink(context.Background(), l)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateLink = %v, ожидалась ErrNotFound", err)
	}
}

func TestSetLinkEnabled(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	id, _ := s.CreateLink(ctx, sampleIn())
	if err := s.SetLinkEnabled(ctx, id, false); err != nil {
		t.Fatalf("SetLinkEnabled: %v", err)
	}

	got, _ := s.Link(ctx, id)
	if got.Enabled {
		t.Error("связка осталась включённой")
	}
	// Остальные поля трогать нельзя: это самое частое действие в таблице.
	if got.Topic != sampleIn().Topic {
		t.Errorf("переключение изменило топик: %q", got.Topic)
	}
}

func TestDeleteLink(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	id, _ := s.CreateLink(ctx, sampleIn())
	if err := s.DeleteLink(ctx, id); err != nil {
		t.Fatalf("DeleteLink: %v", err)
	}
	if _, err := s.Link(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("Link после удаления = %v, ожидалась ErrNotFound", err)
	}
	if err := s.DeleteLink(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("повторное удаление = %v, ожидалась ErrNotFound", err)
	}
}

func TestLinkWithoutDevice(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	// Связка сама по себе, без устройства: внешний ключ должен стать NULL,
	// иначе проверка целостности отвергнет ссылку на устройство с нулём.
	id, err := s.CreateLink(ctx, sampleIn())
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	got, err := s.Link(ctx, id)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if got.DeviceID != 0 {
		t.Errorf("DeviceID = %d, ожидался ноль", got.DeviceID)
	}
}

func TestDeviceCRUD(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	id, err := s.CreateDevice(ctx, Device{
		Name:        "Реле прихожей",
		TopicPrefix: "shellies/shelly25-A1B2C3",
		Model:       "SHSW-25",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	got, err := s.DeviceByPrefix(ctx, "shellies/shelly25-A1B2C3")
	if err != nil {
		t.Fatalf("DeviceByPrefix: %v", err)
	}
	if got.ID != id || got.Model != "SHSW-25" {
		t.Errorf("устройство = %+v", got)
	}
	if got.Online {
		t.Error("новое устройство помечено как присутствующее")
	}

	if err := s.SetDeviceOnline(ctx, id, true); err != nil {
		t.Fatalf("SetDeviceOnline: %v", err)
	}
	got, _ = s.Device(ctx, id)
	if !got.Online || got.LastSeen.IsZero() {
		t.Errorf("присутствие не отмечено: %+v", got)
	}
}

func TestCreateDeviceRequiresName(t *testing.T) {
	s := open(t)
	if _, err := s.CreateDevice(context.Background(), Device{TopicPrefix: "shellies/a"}); err == nil {
		t.Error("устройство без имени сохранено")
	}
}

// Осиротевшие связки в базе никому не нужны: удаление устройства должно
// уносить их с собой.
func TestDeleteDeviceCascadesToLinks(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	deviceID, err := s.CreateDevice(ctx, Device{Name: "Реле", TopicPrefix: "shellies/a"})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	l := sampleIn()
	l.DeviceID = deviceID
	if _, err := s.CreateLink(ctx, l); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	if err := s.DeleteDevice(ctx, deviceID); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}

	links, err := s.Links(ctx)
	if err != nil {
		t.Fatalf("Links: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("после удаления устройства осталось %d связок", len(links))
	}
}

func TestLinksByDevice(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	deviceID, _ := s.CreateDevice(ctx, Device{Name: "Реле", TopicPrefix: "shellies/a"})

	withDevice := sampleIn()
	withDevice.DeviceID = deviceID
	if _, err := s.CreateLink(ctx, withDevice); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if _, err := s.CreateLink(ctx, sampleIn()); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	links, err := s.LinksByDevice(ctx, deviceID)
	if err != nil {
		t.Fatalf("LinksByDevice: %v", err)
	}
	if len(links) != 1 {
		t.Errorf("у устройства %d связок, ожидалась одна", len(links))
	}
}

// Назначение связки должно переживать обход через базу: от него зависит,
// допустим ли retain.
func TestLinkKindRoundTrip(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	id, err := s.CreateLink(ctx, link.Link{
		Enabled: true, Direction: link.Out, Kind: link.KindState,
		Topic: "mimismart/прихожая/температура", Decode: link.DecodeSensor,
		Retain: true, TargetID: 542, TargetSubID: 16,
	})
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	got, err := s.Link(ctx, id)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if got.Kind != link.KindState || !got.Retain {
		t.Errorf("связка вернулась как %+v", got)
	}
}

// Исходящая связка без явного назначения — команда: это строже по проверкам,
// и retain у неё запрещён.
func TestLinkKindDefaultsToCommand(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	id, err := s.CreateLink(ctx, link.Link{
		Enabled: true, Direction: link.Out,
		Topic: "shellies/a/relay/0/command", Decode: link.DecodeLamp,
		TargetID: 563, TargetSubID: 57,
	})
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	got, _ := s.Link(ctx, id)
	if got.Kind != link.KindCommand {
		t.Errorf("назначение = %q, ожидалось %q", got.Kind, link.KindCommand)
	}
}

// У входящей связки назначения нет вовсе: она ничего не публикует, и
// различать команду с показанием ей незачем.
func TestIncomingLinkHasNoKind(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	id, err := s.CreateLink(ctx, sampleIn())
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	got, _ := s.Link(ctx, id)
	if got.Kind != "" {
		t.Errorf("назначение = %q, у входящей связки его быть не должно", got.Kind)
	}
	if got.Extract != link.ExtractRaw {
		t.Errorf("способ извлечения = %q, ожидался %q", got.Extract, link.ExtractRaw)
	}
}

// Развёртывание шаблона: устройство и связки заводятся вместе, канал реле
// разворачивается парой.
func TestApplyTemplate(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	tmpl, err := s.Template(ctx, "shelly25-relay")
	if err != nil {
		t.Fatalf("Template: %v", err)
	}

	deviceID, err := s.ApplyTemplate(ctx,
		Device{Name: "Реле прихожей", TopicPrefix: "shellies/shellyswitch25-4022D8956527"},
		tmpl,
		map[string]devtmpl.Addr{
			"ch0":    {ID: 758, SubID: 4},
			"power0": {ID: 563, SubID: 120},
			"temp":   {ID: 542, SubID: 16},
		})
	if err != nil {
		t.Fatalf("ApplyTemplate: %v", err)
	}

	links, err := s.LinksByDevice(ctx, deviceID)
	if err != nil {
		t.Fatalf("LinksByDevice: %v", err)
	}
	// Пара по каналу 0, мощность, температура — четыре связки.
	if len(links) != 4 {
		t.Fatalf("развернулось %d связок, ожидалось 4: %+v", len(links), links)
	}

	var paired int
	for _, l := range links {
		if l.DeviceID != deviceID {
			t.Errorf("связка %q не привязана к устройству", l.Name)
		}
		if l.PairID != 0 {
			paired++
		}
	}
	if paired != 2 {
		t.Errorf("в паре %d связок, ожидалось 2", paired)
	}
}

// Удаление устройства уносит развёрнутые связки: осиротевшие правила молча
// продолжали бы писать в элементы.
func TestDeleteDeviceRemovesTemplateLinks(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	tmpl, _ := s.Template(ctx, "shelly25-relay")
	deviceID, err := s.ApplyTemplate(ctx,
		Device{Name: "Реле", TopicPrefix: "shellies/sw25-A1"}, tmpl,
		map[string]devtmpl.Addr{"ch0": {ID: 758, SubID: 4}})
	if err != nil {
		t.Fatalf("ApplyTemplate: %v", err)
	}

	if err := s.DeleteDevice(ctx, deviceID); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	links, _ := s.Links(ctx)
	if len(links) != 0 {
		t.Errorf("после удаления устройства осталось %d связок", len(links))
	}
}

// Правка устройства: назначения восстанавливаются, связки разворачиваются
// заново, а чужое не трогается.
func TestReapplyTemplate(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	tmpl, _ := s.Template(ctx, "shelly25-relay")
	deviceID, err := s.ApplyTemplate(ctx,
		Device{Name: "Реле", TopicPrefix: "shellies/sw25-A1"}, tmpl,
		map[string]devtmpl.Addr{"ch0": {ID: 758, SubID: 4}, "power0": {ID: 563, SubID: 120}})
	if err != nil {
		t.Fatalf("ApplyTemplate: %v", err)
	}

	// Назначения должны восстанавливаться из развёрнутых связок.
	assign, err := s.Assignment(ctx, deviceID, tmpl)
	if err != nil {
		t.Fatalf("Assignment: %v", err)
	}
	if assign["ch0"] != (devtmpl.Addr{ID: 758, SubID: 4}) || assign["power0"] != (devtmpl.Addr{ID: 563, SubID: 120}) {
		t.Errorf("восстановлено %+v", assign)
	}
	if _, ok := assign["temp"]; ok {
		t.Error("роль без связки попала в назначения")
	}

	// Человек выключил одну связку и завёл свою.
	links, _ := s.LinksByDevice(ctx, deviceID)
	for _, l := range links {
		if l.Name == "Канал 0 — мощность" {
			if err := s.SetLinkEnabled(ctx, l.ID, false); err != nil {
				t.Fatalf("SetLinkEnabled: %v", err)
			}
		}
	}
	own := sampleIn()
	own.Name, own.DeviceID, own.Topic = "Своя связка", deviceID, "своя/связка"
	if _, err := s.CreateLink(ctx, own); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	// Переназначаем канал и добавляем температуру.
	err = s.ReapplyTemplate(ctx,
		Device{ID: deviceID, Name: "Реле прихожей", TopicPrefix: "shellies/sw25-A1"}, tmpl,
		map[string]devtmpl.Addr{
			"ch0": {ID: 758, SubID: 5}, "power0": {ID: 563, SubID: 120}, "temp": {ID: 542, SubID: 16},
		})
	if err != nil {
		t.Fatalf("ReapplyTemplate: %v", err)
	}

	after, _ := s.LinksByDevice(ctx, deviceID)
	var own2, power, temp *link.Link
	for i := range after {
		switch after[i].Name {
		case "Своя связка":
			own2 = &after[i]
		case "Канал 0 — мощность":
			power = &after[i]
		case "Температура устройства":
			temp = &after[i]
		}
	}
	if own2 == nil {
		t.Error("связка, заведённая вручную, снесена перенастройкой")
	}
	if temp == nil {
		t.Error("добавленная роль не развернулась")
	}
	// Выключенное человеком не должно включаться обратно правкой назначений.
	if power == nil || power.Enabled {
		t.Error("выключенная связка включилась сама")
	}
	for _, l := range after {
		if l.Name == "Канал 0 — состояние" && l.TargetSubID != 5 {
			t.Errorf("канал не переехал: %s", l.Addr())
		}
	}

	// Устройство остаётся тем же, а не заводится заново.
	devices, _ := s.Devices(ctx)
	if len(devices) != 1 || devices[0].Name != "Реле прихожей" {
		t.Errorf("устройства: %+v", devices)
	}
}

// Неверное назначение не должно оставлять устройство без связок.
func TestReapplyValidatesBeforeDeleting(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	tmpl, _ := s.Template(ctx, "shelly25-relay")
	deviceID, _ := s.ApplyTemplate(ctx,
		Device{Name: "Реле", TopicPrefix: "shellies/sw25-A1"}, tmpl,
		map[string]devtmpl.Addr{"ch0": {ID: 758, SubID: 4}})

	before, _ := s.LinksByDevice(ctx, deviceID)
	// Обязательная роль без элемента — шаблон не применится.
	err := s.ReapplyTemplate(ctx,
		Device{ID: deviceID, Name: "Реле", TopicPrefix: "shellies/sw25-A1"}, tmpl,
		map[string]devtmpl.Addr{"temp": {ID: 542, SubID: 16}})
	if err == nil {
		t.Fatal("перенастройка без обязательной роли прошла")
	}

	after, _ := s.LinksByDevice(ctx, deviceID)
	if len(after) != len(before) {
		t.Errorf("связок было %d, стало %d — неудачная правка их снесла", len(before), len(after))
	}
}
