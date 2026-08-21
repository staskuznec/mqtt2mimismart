package mqtt

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewRequiresAddr(t *testing.T) {
	if _, err := New(Config{}, testLogger()); err == nil {
		t.Error("пустой адрес брокера принят")
	}
}

// Идентификатор обязан быть уникальным на брокере: два клиента с одним и тем
// же выбивают друг друга по очереди, и это выглядит как мигающая связь.
func TestNewFillsClientID(t *testing.T) {
	c, err := New(Config{Addr: "127.0.0.1:1883"}, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Status().ClientID == "" {
		t.Error("идентификатор клиента пуст")
	}
}

func TestNewKeepsGivenClientID(t *testing.T) {
	c, err := New(Config{Addr: "127.0.0.1:1883", ClientID: "свой-идентификатор"}, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.Status().ClientID; got != "свой-идентификатор" {
		t.Errorf("ClientID = %q, ожидался заданный", got)
	}
}

func TestKindOf(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    string
	}{
		{"пусто", "", KindEmpty},
		{"пробелы", "   ", KindEmpty},
		{"мощность", "41.23", KindNumber},
		{"целое", "1", KindNumber},
		{"отрицательное", "-3.5", KindNumber},
		{"объект", `{"ison":true,"power":41.2}`, KindJSON},
		{"массив", `[1,2,3]`, KindJSON},
		{"состояние реле", "on", KindText},
		{"перегрузка", "overpower", KindText},
		{"битый json", `{"ison":`, KindText},
		// "123" разбирается и как число, и как JSON. Показывать его деревом
		// полей бессмысленно, поэтому число важнее.
		{"число важнее json", "123", KindNumber},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := kindOf([]byte(tc.payload)); got != tc.want {
				t.Errorf("kindOf(%q) = %q, ожидалось %q", tc.payload, got, tc.want)
			}
		})
	}
}

func TestRegistryObserve(t *testing.T) {
	r := newRegistry()
	now := time.Now()

	r.observe(Message{Topic: "shellies/shelly25-A1B2C3/relay/0", Payload: []byte("on"), At: now})
	r.observe(Message{Topic: "shellies/shelly25-A1B2C3/relay/0", Payload: []byte("off"), At: now.Add(time.Second)})

	info, ok := r.Topic("shellies/shelly25-A1B2C3/relay/0")
	if !ok {
		t.Fatal("топик не запомнен")
	}
	if info.Count != 2 {
		t.Errorf("Count = %d, ожидалось 2", info.Count)
	}
	if string(info.LastPayload) != "off" {
		t.Errorf("LastPayload = %q, ожидалось %q", info.LastPayload, "off")
	}
	if info.Kind != KindText {
		t.Errorf("Kind = %q, ожидалось %q", info.Kind, KindText)
	}
	if !info.FirstAt.Equal(now) {
		t.Errorf("FirstAt = %v, ожидалось время первого сообщения", info.FirstAt)
	}
}

// Библиотека переиспользует буфер нагрузки, поэтому реестр обязан хранить
// копию: иначе последнее значение будет меняться само по себе.
func TestRegistryCopiesPayload(t *testing.T) {
	r := newRegistry()
	payload := []byte("on")

	r.observe(Message{Topic: "тест", Payload: payload, At: time.Now()})
	payload[0], payload[1] = 'X', 'X'

	info, _ := r.Topic("тест")
	if string(info.LastPayload) != "on" {
		t.Errorf("LastPayload = %q — реестр держит чужой буфер, а не копию", info.LastPayload)
	}
}

func TestRegistryTruncatesLongPayload(t *testing.T) {
	r := newRegistry()
	long := make([]byte, maxPayloadKept+100)
	for i := range long {
		long[i] = 'a'
	}

	r.observe(Message{Topic: "длинный", Payload: long, At: time.Now()})

	info, _ := r.Topic("длинный")
	if len(info.LastPayload) != maxPayloadKept {
		t.Errorf("сохранено %d байт, ожидалось %d", len(info.LastPayload), maxPayloadKept)
	}
	if !info.Truncated {
		t.Error("обрезка не отмечена флагом Truncated")
	}
}

// Шина чужая, и сколько на ней топиков, заранее неизвестно. Предел не должен
// ронять приём — только перестать запоминать новые.
func TestRegistryOverflow(t *testing.T) {
	r := newRegistry()
	now := time.Now()

	for i := 0; i < maxTopics+10; i++ {
		r.observe(Message{Topic: "топик/" + string(rune('a'+i%26)) + itoa(i), Payload: []byte("1"), At: now})
	}

	known, overflow := r.stats()
	if known != maxTopics {
		t.Errorf("запомнено %d топиков, ожидался предел %d", known, maxTopics)
	}
	if overflow != 10 {
		t.Errorf("незапомненных %d, ожидалось 10", overflow)
	}
}

// Уже известный топик должен обновляться и после достижения предела: иначе
// на забитой шине связка перестанет видеть свежие значения.
func TestRegistryUpdatesKnownTopicAfterOverflow(t *testing.T) {
	r := newRegistry()
	now := time.Now()

	r.observe(Message{Topic: "важный", Payload: []byte("on"), At: now})
	for i := 0; i < maxTopics+10; i++ {
		r.observe(Message{Topic: "мусор/" + itoa(i), Payload: []byte("1"), At: now})
	}
	r.observe(Message{Topic: "важный", Payload: []byte("off"), At: now.Add(time.Minute)})

	info, ok := r.Topic("важный")
	if !ok {
		t.Fatal("известный топик потерян")
	}
	if string(info.LastPayload) != "off" {
		t.Errorf("LastPayload = %q, ожидалось %q", info.LastPayload, "off")
	}
}

func TestRegistryTopicsSortedByRecency(t *testing.T) {
	r := newRegistry()
	now := time.Now()

	r.observe(Message{Topic: "старый", Payload: []byte("1"), At: now})
	r.observe(Message{Topic: "свежий", Payload: []byte("1"), At: now.Add(time.Minute)})

	topics := r.Topics()
	if len(topics) != 2 {
		t.Fatalf("топиков %d, ожидалось 2", len(topics))
	}
	if topics[0].Topic != "свежий" {
		t.Errorf("первым идёт %q, ожидался самый свежий", topics[0].Topic)
	}
}

func TestRegistryForget(t *testing.T) {
	r := newRegistry()
	r.observe(Message{Topic: "тест", Payload: []byte("1"), At: time.Now()})

	r.forgetAll()

	if known, overflow := r.stats(); known != 0 || overflow != 0 {
		t.Errorf("после очистки known=%d overflow=%d, ожидались нули", known, overflow)
	}
}

// itoa без strconv: в тестах нужен только короткий суффикс к имени топика.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Скрытый топик не запоминается вовсе. Иначе смысла в скрытии нет: чужой
// датчик публикует каждые несколько секунд и вернулся бы на страницу сразу.
func TestRegistrySkipsHidden(t *testing.T) {
	r := newRegistry()
	now := time.Now()

	r.setHidden([]Hidden{{Pattern: "чужое/датчик", Tree: true}})
	r.observe(Message{Topic: "чужое/датчик/температура", Payload: []byte("21"), At: now})
	r.observe(Message{Topic: "своё/реле", Payload: []byte("on"), At: now})

	if _, ok := r.Topic("чужое/датчик/температура"); ok {
		t.Error("скрытый топик запомнен")
	}
	if _, ok := r.Topic("своё/реле"); !ok {
		t.Error("свой топик потерян вместе с чужими")
	}
}

// Скрытие убирает и то, что снифер уже запомнил: иначе топик оставался бы на
// странице до перезапуска службы, и кнопка выглядела бы сломанной.
func TestSetHiddenDropsRemembered(t *testing.T) {
	r := newRegistry()
	r.observe(Message{Topic: "чужое/датчик/температура", Payload: []byte("21"), At: time.Now()})

	r.setHidden([]Hidden{{Pattern: "чужое/датчик", Tree: true}})

	if known, _ := r.stats(); known != 0 {
		t.Errorf("после скрытия в снифере %d топиков, ожидался ноль", known)
	}
}

// Мастер-топик режется по границе уровня. Общее начало строки — не родство:
// "shellies/shelly1" не должен утащить с собой соседний "shellies/shelly1pm-a8".
func TestHiddenKeepsLevelBoundary(t *testing.T) {
	r := newRegistry()
	now := time.Now()

	r.observe(Message{Topic: "shellies/shelly1/relay/0", Payload: []byte("on"), At: now})
	r.observe(Message{Topic: "shellies/shelly1pm-a8/relay/0", Payload: []byte("on"), At: now})

	if n := r.forget(Hidden{Pattern: "shellies/shelly1", Tree: true}); n != 1 {
		t.Errorf("убрано топиков: %d, ожидался один", n)
	}
	if _, ok := r.Topic("shellies/shelly1pm-a8/relay/0"); !ok {
		t.Error("сосед с общим началом имени убран заодно")
	}
}

// Убранный топик возвращается со следующим сообщением: правило при этом не
// запоминается, и остаток от снятого с шины устройства просто исчезает.
func TestForgetLetsTopicComeBack(t *testing.T) {
	r := newRegistry()
	now := time.Now()

	r.observe(Message{Topic: "живое/реле", Payload: []byte("on"), At: now})
	r.forget(Hidden{Pattern: "живое/реле"})
	r.observe(Message{Topic: "живое/реле", Payload: []byte("off"), At: now.Add(time.Second)})

	if _, ok := r.Topic("живое/реле"); !ok {
		t.Error("топик не вернулся после нового сообщения — убирание сработало как скрытие")
	}
}
