package link

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	shclient "github.com/staskuznec/shClientMimismart"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSender запоминает отправленное в умный дом.
type fakeSender struct {
	mu   sync.Mutex
	sent int
	err  error
}

func (f *fakeSender) Send(_ context.Context, values ...shclient.Value) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent += len(values)
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent
}

// fakePublisher запоминает опубликованное на шину.
type fakePublisher struct {
	mu   sync.Mutex
	msgs []published
	err  error
}

type published struct {
	topic   string
	payload string
	qos     byte
	retain  bool
}

func (f *fakePublisher) Publish(topic string, payload []byte, qos byte, retain bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.msgs = append(f.msgs, published{topic, string(payload), qos, retain})
	return nil
}

func (f *fakePublisher) all() []published {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]published(nil), f.msgs...)
}

func newTestEngine() (*Engine, *fakeSender, *fakePublisher) {
	sh, mq := &fakeSender{}, &fakePublisher{}
	return NewEngine(sh, mq, testLogger()), sh, mq
}

// Основной путь: реле отчиталось о состоянии, значение легло в лампу.
func TestOnMessageDeliversToSmartHome(t *testing.T) {
	e, sh, _ := newTestEngine()
	e.SetLinks([]Link{{
		ID: 1, Enabled: true, Direction: In,
		Topic:    "shellies/shelly25-A1/relay/0",
		Encode:   EncodeByte,
		Values:   map[string]string{"on": "1", "off": "0"},
		TargetID: 563, TargetSubID: 57,
	}})

	e.OnMessage(context.Background(), "shellies/shelly25-A1/relay/0", []byte("on"))

	if sh.count() != 1 {
		t.Fatalf("отправлено %d значений, ожидалось 1", sh.count())
	}
	st := e.Stats()[1]
	if st.Delivered != 1 || st.LastValue != "1" {
		t.Errorf("счётчики связки = %+v", st)
	}
}

// Отключённая связка не должна срабатывать вовсе.
func TestDisabledLinkIgnored(t *testing.T) {
	e, sh, _ := newTestEngine()
	e.SetLinks([]Link{{
		ID: 1, Enabled: false, Direction: In,
		Topic: "shellies/#", Encode: EncodeByte,
	}})

	e.OnMessage(context.Background(), "shellies/a/relay/0", []byte("1"))

	if sh.count() != 0 {
		t.Error("отключённая связка сработала")
	}
}

// Одно сообщение может кормить несколько связок: то же состояние идёт и в
// лампу, и в текстовый датчик диагностики.
func TestOnMessageFeedsAllMatchingLinks(t *testing.T) {
	e, sh, _ := newTestEngine()
	e.SetLinks([]Link{
		{ID: 1, Enabled: true, Direction: In, Topic: "shellies/+/relay/0",
			Encode: EncodeByte, Values: map[string]string{"on": "1"}, TargetID: 563, TargetSubID: 57},
		{ID: 2, Enabled: true, Direction: In, Topic: "shellies/shelly25-A1/relay/0",
			Encode: EncodeText, TargetID: 563, TargetSubID: 120},
	})

	e.OnMessage(context.Background(), "shellies/shelly25-A1/relay/0", []byte("on"))

	if sh.count() != 2 {
		t.Errorf("отправлено %d значений, ожидалось 2", sh.count())
	}
}

// По таймеру устройства шлют одно и то же. Без отсева в умный дом каждую
// секунду уходили бы одинаковые значения.
func TestOnlyChangedSkipsRepeats(t *testing.T) {
	e, sh, _ := newTestEngine()
	e.SetLinks([]Link{{
		ID: 1, Enabled: true, Direction: In, OnlyChanged: true,
		Topic: "shellies/a/meter/0", Encode: EncodeText,
		TargetID: 563, TargetSubID: 120,
	}})

	ctx := context.Background()
	e.OnMessage(ctx, "shellies/a/meter/0", []byte("41.2"))
	e.OnMessage(ctx, "shellies/a/meter/0", []byte("41.2"))
	e.OnMessage(ctx, "shellies/a/meter/0", []byte("41.3"))

	if sh.count() != 2 {
		t.Errorf("отправлено %d значений, ожидалось 2 (повтор отсеивается)", sh.count())
	}
	if st := e.Stats()[1]; st.Skipped != 1 {
		t.Errorf("отсеяно %d, ожидалось 1", st.Skipped)
	}
}

func TestOnlyChangedDisabledSendsEverything(t *testing.T) {
	e, sh, _ := newTestEngine()
	e.SetLinks([]Link{{
		ID: 1, Enabled: true, Direction: In, OnlyChanged: false,
		Topic: "shellies/a/meter/0", Encode: EncodeText,
		TargetID: 563, TargetSubID: 120,
	}})

	ctx := context.Background()
	e.OnMessage(ctx, "shellies/a/meter/0", []byte("41.2"))
	e.OnMessage(ctx, "shellies/a/meter/0", []byte("41.2"))

	if sh.count() != 2 {
		t.Errorf("отправлено %d значений, ожидалось 2", sh.count())
	}
}

// Нажатие лампы в интерфейсе превращается в команду устройству.
func TestOnEventPublishesCommand(t *testing.T) {
	e, _, mq := newTestEngine()
	e.SetLinks([]Link{{
		ID: 2, Enabled: true, Direction: Out,
		Topic: "shellies/shelly25-A1/relay/0/command", QoS: 1,
		Decode:   DecodeLamp,
		Values:   map[string]string{StateToggle: "toggle", StateOn: "on", StateOff: "off"},
		TargetID: 563, TargetSubID: 57,
	}})

	e.OnEvent(context.Background(), Event{ID: 563, SubID: 57, Payload: []byte{0xFF}})

	msgs := mq.all()
	if len(msgs) != 1 {
		t.Fatalf("опубликовано %d сообщений, ожидалось 1", len(msgs))
	}
	if msgs[0].payload != "toggle" {
		t.Errorf("нагрузка = %q, ожидалось %q", msgs[0].payload, "toggle")
	}
	if msgs[0].retain {
		t.Error("команда опубликована с retain — устройство щёлкнет само при включении")
	}
}

// Снимок состояний приходит теми же пакетами, что и нажатие. Если пустить его
// наружу, шлюз при каждом подключении щёлкнет всеми реле на объекте.
func TestSyncEventsNeverPublish(t *testing.T) {
	e, _, mq := newTestEngine()
	e.SetLinks([]Link{{
		ID: 2, Enabled: true, Direction: Out,
		Topic:  "shellies/shelly25-A1/relay/0/command",
		Decode: DecodeLamp, TargetID: 563, TargetSubID: 57,
	}})

	e.OnEvent(context.Background(), Event{ID: 563, SubID: 57, Payload: []byte{1}, Sync: true})

	if len(mq.all()) != 0 {
		t.Error("событие синхронизации ушло на шину командой")
	}
}

// Петля: записали в лампу единицу, сервер вернул её же событием. Без защиты
// шлюз опубликовал бы команду устройству, которое и так включено.
func TestEchoOfOwnWriteIsNotRepublished(t *testing.T) {
	e, _, mq := newTestEngine()
	e.SetLinks([]Link{
		{ID: 1, Enabled: true, Direction: In,
			Topic: "shellies/shelly25-A1/relay/0", Encode: EncodeByte,
			Values: map[string]string{"on": "1"}, TargetID: 563, TargetSubID: 57},
		{ID: 2, Enabled: true, Direction: Out,
			Topic: "shellies/shelly25-A1/relay/0/command", Decode: DecodeLamp,
			Values: map[string]string{StateOn: "on"}, TargetID: 563, TargetSubID: 57},
	})

	ctx := context.Background()
	// Устройство отчиталось — пишем в лампу один байт 0x01.
	e.OnMessage(ctx, "shellies/shelly25-A1/relay/0", []byte("on"))
	// Сервер вернул нашу же запись событием.
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{1}})

	if msgs := mq.all(); len(msgs) != 0 {
		t.Errorf("эхо собственной записи ушло командой: %+v", msgs)
	}
	if st := e.Stats()[2]; st.Echoes != 1 {
		t.Errorf("эхо не отмечено в счётчиках: %+v", st)
	}
}

// Настоящее нажатие после нашей записи обязано пройти: защита от эха ловит
// совпадение значения, а не любое событие подряд.
func TestDifferentValueAfterOwnWritePasses(t *testing.T) {
	e, _, mq := newTestEngine()
	e.SetLinks([]Link{
		{ID: 1, Enabled: true, Direction: In,
			Topic: "shellies/shelly25-A1/relay/0", Encode: EncodeByte,
			Values: map[string]string{"on": "1"}, TargetID: 563, TargetSubID: 57},
		{ID: 2, Enabled: true, Direction: Out,
			Topic: "shellies/shelly25-A1/relay/0/command", Decode: DecodeLamp,
			Values: map[string]string{StateOff: "off"}, TargetID: 563, TargetSubID: 57},
	})

	ctx := context.Background()
	e.OnMessage(ctx, "shellies/shelly25-A1/relay/0", []byte("on")) // записали 0x01
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{0}})  // человек выключил

	msgs := mq.all()
	if len(msgs) != 1 {
		t.Fatalf("опубликовано %d сообщений, ожидалось 1", len(msgs))
	}
	if msgs[0].payload != "off" {
		t.Errorf("нагрузка = %q, ожидалось %q", msgs[0].payload, "off")
	}
}

// Событие по элементу, к которому связок нет, не должно ничего делать.
func TestUnknownElementIgnored(t *testing.T) {
	e, _, mq := newTestEngine()
	e.SetLinks([]Link{{
		ID: 2, Enabled: true, Direction: Out,
		Topic: "shellies/a/relay/0/command", Decode: DecodeLamp,
		TargetID: 563, TargetSubID: 57,
	}})

	e.OnEvent(context.Background(), Event{ID: 999, SubID: 1, Payload: []byte{1}})

	if len(mq.all()) != 0 {
		t.Error("событие по чужому элементу породило публикацию")
	}
}

// Ошибка преобразования не должна ронять движок и обязана быть видна в вебе.
func TestTransformErrorRecorded(t *testing.T) {
	e, sh, _ := newTestEngine()
	e.SetLinks([]Link{{
		ID: 1, Enabled: true, Direction: In,
		Topic: "shellies/a/relay/0", Encode: EncodeByte,
		TargetID: 563, TargetSubID: 57,
	}})

	e.OnMessage(context.Background(), "shellies/a/relay/0", []byte("странное"))

	if sh.count() != 0 {
		t.Error("неразобранное значение всё же отправлено")
	}
	st := e.Stats()[1]
	if st.Errors != 1 || st.LastError == "" {
		t.Errorf("ошибка не записана в счётчики: %+v", st)
	}
}

// Неудачная отправка не должна попадать в кэш последних значений: иначе
// значение больше никогда не отправится, и элемент останется устаревшим.
func TestFailedSendIsNotRemembered(t *testing.T) {
	e, sh, _ := newTestEngine()
	sh.err = context.DeadlineExceeded
	e.SetLinks([]Link{{
		ID: 1, Enabled: true, Direction: In, OnlyChanged: true,
		Topic: "shellies/a/relay/0", Encode: EncodeByte,
		Values: map[string]string{"on": "1"}, TargetID: 563, TargetSubID: 57,
	}})

	ctx := context.Background()
	e.OnMessage(ctx, "shellies/a/relay/0", []byte("on"))

	sh.mu.Lock()
	sh.err = nil
	sh.mu.Unlock()

	e.OnMessage(ctx, "shellies/a/relay/0", []byte("on"))

	if sh.count() != 1 {
		t.Errorf("после сбоя значение не отправилось повторно: отправлено %d", sh.count())
	}
}

// Кэш последних значений не должен расти бесконечно при правках в вебе.
func TestSetLinksForgetsRemovedLinks(t *testing.T) {
	e, _, _ := newTestEngine()
	l := Link{
		ID: 1, Enabled: true, Direction: In, OnlyChanged: true,
		Topic: "shellies/a/relay/0", Encode: EncodeByte,
		Values: map[string]string{"on": "1"}, TargetID: 563, TargetSubID: 57,
	}
	e.SetLinks([]Link{l})
	e.OnMessage(context.Background(), "shellies/a/relay/0", []byte("on"))

	e.SetLinks(nil) // связку удалили в вебе

	e.mu.RLock()
	remembered := len(e.last)
	e.mu.RUnlock()
	if remembered != 0 {
		t.Errorf("в кэше осталось %d значений удалённых связок", remembered)
	}
}

// Состояния приезжают в каждом ответе на запрос состояний. Без отсева шлюз
// слал бы команду по каждой лампе на каждом опросе — несколько раз в минуту
// без всякой причины.
func TestOutSkipsRepeatedStates(t *testing.T) {
	e, _, mq := newTestEngine()
	e.SetLinks([]Link{{
		ID: 2, Enabled: true, Direction: Out, OnlyChanged: true,
		Topic: "shellies/a/relay/0/command", Decode: DecodeLamp,
		Values:   map[string]string{StateOn: "on", StateOff: "off"},
		TargetID: 563, TargetSubID: 57,
	}})

	ctx := context.Background()
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{1}}) // включено
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{1}}) // тот же снимок
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{8}}) // включено, но от выключателя
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{0}}) // выключили

	msgs := mq.all()
	if len(msgs) != 2 {
		t.Fatalf("опубликовано %d сообщений, ожидалось 2 (on и off)", len(msgs))
	}
	if msgs[0].payload != "on" || msgs[1].payload != "off" {
		t.Errorf("опубликовано %+v", msgs)
	}
	if st := e.Stats()[2]; st.Skipped != 2 {
		t.Errorf("отсеяно %d повторов, ожидалось 2", st.Skipped)
	}
}

// Нажатие — не состояние. Нажали дважды, значит переключить надо дважды, и
// отсев повторов здесь проглотил бы второе нажатие.
func TestOutAlwaysPassesToggle(t *testing.T) {
	e, _, mq := newTestEngine()
	e.SetLinks([]Link{{
		ID: 2, Enabled: true, Direction: Out, OnlyChanged: true,
		Topic: "shellies/a/relay/0/command", Decode: DecodeLamp,
		Values:   map[string]string{StateToggle: "toggle"},
		TargetID: 563, TargetSubID: 57,
	}})

	ctx := context.Background()
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{0xFF}})
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{0xFF}})

	if msgs := mq.all(); len(msgs) != 2 {
		t.Errorf("опубликовано %d сообщений, ожидалось 2 — второе нажатие проглочено", len(msgs))
	}
}

// Связка живёт в базе шлюза и переживает правку logic.xml: элемент удалили или
// перенумеровали, а она осталась. Писать в такой адрес нельзя — в лучшем случае
// пакет пропадёт, в худшем по освободившемуся адресу успели завести другой
// элемент, и туда поедут чужие значения.
func TestEngineSkipsMissingElement(t *testing.T) {
	sender := &fakeSender{}
	e := NewEngine(sender, &fakePublisher{}, testLogger())
	e.SetLinks([]Link{{
		ID: 1, Enabled: true, Direction: In, Name: "Заряд",
		Topic: "microart/inv1/bat/0/C_100_remain", Encode: EncodeText,
		TargetID: 563, TargetSubID: 160,
	}})

	// Описание дома есть, но этого адреса в нём нет.
	e.SetKnownAddrs(func(addr string) bool { return addr == "563:116" })

	e.OnMessage(context.Background(), "microart/inv1/bat/0/C_100_remain", []byte("88"))

	if n := sender.count(); n != 0 {
		t.Fatalf("отправлено %d значений в несуществующий элемент", n)
	}
	st := e.Stats()[1]
	if st.Errors != 1 {
		t.Errorf("ошибок %d, ожидалась одна", st.Errors)
	}
	if !strings.Contains(st.LastError, "563:160") {
		t.Errorf("в ошибке нет адреса: %q", st.LastError)
	}

	// Элемент завели обратно — связка работает как ни в чём не бывало.
	e.SetKnownAddrs(func(addr string) bool { return true })
	e.OnMessage(context.Background(), "microart/inv1/bat/0/C_100_remain", []byte("88"))
	if sender.count() != 1 {
		t.Errorf("после появления элемента отправлено %d значений", sender.count())
	}
}

// Нажатие уезжает устройству абсолютной командой, а не «переключить»:
// устройство отчиталось «включено», значит нажатие означает «выключить».
func TestToggleBecomesAbsoluteCommand(t *testing.T) {
	e, _, mq := newTestEngine()
	e.SetLinks([]Link{
		{ID: 1, Enabled: true, Direction: In,
			Topic: "shellies/shelly25-A1/relay/0", Encode: EncodeByte,
			Values:   map[string]string{"on": "1", "off": "0"},
			TargetID: 563, TargetSubID: 57},
		{ID: 2, Enabled: true, Direction: Out,
			Topic: "shellies/shelly25-A1/relay/0/command", Decode: DecodeLamp,
			Values:   map[string]string{StateToggle: "toggle", StateOn: "on", StateOff: "off"},
			TargetID: 563, TargetSubID: 57},
	})

	ctx := context.Background()
	e.OnMessage(ctx, "shellies/shelly25-A1/relay/0", []byte("on"))
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{0xFF}})

	msgs := mq.all()
	if len(msgs) != 1 {
		t.Fatalf("опубликовано %d сообщений, ожидалось 1", len(msgs))
	}
	if msgs[0].payload != "off" {
		t.Errorf("нагрузка = %q, ожидалось %q: относительная команда на слабом WiFi уводит фазу",
			msgs[0].payload, "off")
	}
}

// Пока устройство молчит, повторное нажатие повторяет ту же команду, а не шлёт
// обратную. Именно это чинит потерянную публикацию: нажали второй раз — и реле
// всё-таки приехало в нужное положение.
func TestToggleRepeatsUntilDeviceReports(t *testing.T) {
	e, _, mq := newTestEngine()
	e.SetLinks([]Link{
		{ID: 1, Enabled: true, Direction: In,
			Topic: "shellies/shelly25-A1/relay/0", Encode: EncodeByte,
			Values:   map[string]string{"on": "1", "off": "0"},
			TargetID: 563, TargetSubID: 57},
		{ID: 2, Enabled: true, Direction: Out, OnlyChanged: true,
			Topic: "shellies/shelly25-A1/relay/0/command", Decode: DecodeLamp,
			Values:   map[string]string{StateToggle: "toggle", StateOn: "on", StateOff: "off"},
			TargetID: 563, TargetSubID: 57},
	})

	ctx := context.Background()
	e.OnMessage(ctx, "shellies/shelly25-A1/relay/0", []byte("off"))
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{0xFF}})
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{0xFF}})

	msgs := mq.all()
	if len(msgs) != 2 {
		t.Fatalf("опубликовано %d сообщений, ожидалось 2: нажатие не отсеивается как повтор", len(msgs))
	}
	for i, m := range msgs {
		if m.payload != "on" {
			t.Errorf("нажатие %d ушло как %q, ожидалось %q", i+1, m.payload, "on")
		}
	}
}

// Устройство отчиталось «включено», а потом само выключилось — состояние
// берётся свежее, и нажатие означает «включить».
func TestAbsoluteCommandFollowsDevice(t *testing.T) {
	e, _, mq := newTestEngine()
	e.SetLinks([]Link{
		{ID: 1, Enabled: true, Direction: In,
			Topic: "shellies/shelly25-A1/relay/0", Encode: EncodeByte,
			Values:   map[string]string{"on": "1", "off": "0"},
			TargetID: 563, TargetSubID: 57},
		{ID: 2, Enabled: true, Direction: Out,
			Topic: "shellies/shelly25-A1/relay/0/command", Decode: DecodeLamp,
			Values:   map[string]string{StateToggle: "toggle", StateOn: "on", StateOff: "off"},
			TargetID: 563, TargetSubID: 57},
	})

	ctx := context.Background()
	e.OnMessage(ctx, "shellies/shelly25-A1/relay/0", []byte("on"))
	e.OnMessage(ctx, "shellies/shelly25-A1/relay/0", []byte("off"))
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{0xFF}})

	msgs := mq.all()
	if len(msgs) != 1 {
		t.Fatalf("опубликовано %d сообщений, ожидалось 1", len(msgs))
	}
	if msgs[0].payload != "on" {
		t.Errorf("нагрузка = %q, ожидалось %q", msgs[0].payload, "on")
	}
}

// Устройство знает только «переключить» — выдумывать за него абсолютную
// команду нельзя: строка уедет на шину и не сделает ничего.
func TestToggleStaysWhenDeviceHasNoAbsolute(t *testing.T) {
	e, _, mq := newTestEngine()
	e.SetLinks([]Link{
		{ID: 1, Enabled: true, Direction: In,
			Topic: "zigbee2mqtt/выключатель", Encode: EncodeByte,
			Values:   map[string]string{"ON": "1"},
			TargetID: 563, TargetSubID: 57},
		{ID: 2, Enabled: true, Direction: Out,
			Topic: "zigbee2mqtt/выключатель/set", Decode: DecodeLamp,
			Values:   map[string]string{StateToggle: `{"state":"TOGGLE"}`},
			TargetID: 563, TargetSubID: 57},
	})

	ctx := context.Background()
	e.OnMessage(ctx, "zigbee2mqtt/выключатель", []byte("ON"))
	e.OnEvent(ctx, Event{ID: 563, SubID: 57, Payload: []byte{0xFF}})

	msgs := mq.all()
	if len(msgs) != 1 {
		t.Fatalf("опубликовано %d сообщений, ожидалось 1", len(msgs))
	}
	if msgs[0].payload != `{"state":"TOGGLE"}` {
		t.Errorf("нагрузка = %q, ожидалось переключение как было", msgs[0].payload)
	}
}
