package link

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	shclient "github.com/staskuznec/shClientMimismart"
)

// echoWindow — сколько собственная запись в элемент считается «своей».
//
// Сервер умного дома транслирует обратно и то, что записали мы сами. Без этого
// окна получалась бы петля: записали в лампу единицу, получили её же обратно
// как событие, опубликовали команду устройству, устройство отчиталось о
// состоянии — и по кругу. Полторы секунды с запасом покрывают оборот пакета
// даже на загруженном сервере.
const echoWindow = 1500 * time.Millisecond

// Sender отправляет значения в умный дом. Интерфейс узкий намеренно: движок
// не должен знать ни про переподключения, ни про рукопожатие.
type Sender interface {
	Send(ctx context.Context, values ...shclient.Value) error
}

// Publisher публикует сообщения на шину.
type Publisher interface {
	Publish(topic string, payload []byte, qos byte, retain bool) error
}

// Event — изменение элемента умного дома, пришедшее от сервера.
type Event struct {
	ID      uint16
	SubID   uint8
	Payload []byte

	// Sync означает, что событие пришло в ответ на запрос состояний, то есть
	// является частью снимка. Такие события наружу не транслируются.
	Sync bool
}

// Addr возвращает адрес элемента в виде "id:subid".
func (e Event) Addr() string { return fmt.Sprintf("%d:%d", e.ID, e.SubID) }

// Stats — счётчики одной связки для веб-интерфейса.
type Stats struct {
	Matched   uint64 // сколько раз связка сработала
	Delivered uint64 // сколько значений доставлено
	Skipped   uint64 // отброшено как неизменившееся
	Echoes    uint64 // отброшено как собственная запись
	Errors    uint64
	LastValue string
	LastAt    time.Time
	LastError string
}

// Engine сводит сообщения с шины и события умного дома со связками.
type Engine struct {
	log *slog.Logger
	sh  Sender
	mq  Publisher

	mu    sync.RWMutex
	in    []Link
	out   map[string][]Link
	last  map[int64]string // последнее отправленное значение связки
	echo  map[string]echoEntry
	stats map[int64]*Stats
}

// echoEntry — что и когда мы сами записали в элемент.
type echoEntry struct {
	payload string // байты в hex
	at      time.Time
}

// NewEngine создаёт движок. Связки задаются отдельно, через SetLinks:
// они меняются в вебе и перечитываются без перезапуска демона.
func NewEngine(sh Sender, mq Publisher, log *slog.Logger) *Engine {
	return &Engine{
		log:   log,
		sh:    sh,
		mq:    mq,
		out:   make(map[string][]Link),
		last:  make(map[int64]string),
		echo:  make(map[string]echoEntry),
		stats: make(map[int64]*Stats),
	}
}

// SetLinks заменяет набор связок целиком.
//
// Отключённые связки не попадают в работу вовсе, а их счётчики сохраняются:
// выключили на время — история осталась.
func (e *Engine) SetLinks(links []Link) {
	in := make([]Link, 0, len(links))
	out := make(map[string][]Link)

	for _, l := range links {
		if !l.Enabled {
			continue
		}
		switch l.Direction {
		case In:
			in = append(in, l)
		case Out:
			out[l.Addr()] = append(out[l.Addr()], l)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.in, e.out = in, out

	// Кэш последних значений чистим по связкам, которых больше нет: иначе он
	// растёт бесконечно, а при возврате удалённой связки хранил бы прошлое.
	alive := make(map[int64]struct{}, len(links))
	for _, l := range links {
		alive[l.ID] = struct{}{}
	}
	for id := range e.last {
		if _, ok := alive[id]; !ok {
			delete(e.last, id)
		}
	}
}

// Stats отдаёт счётчики по связкам.
func (e *Engine) Stats() map[int64]Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make(map[int64]Stats, len(e.stats))
	for id, s := range e.stats {
		out[id] = *s
	}
	return out
}

// OnMessage разбирает сообщение с шины и раскладывает его по элементам.
// Одно сообщение может кормить несколько связок — например, состояние реле
// идёт и в лампу, и в текстовый датчик диагностики.
func (e *Engine) OnMessage(ctx context.Context, topic string, payload []byte) {
	e.mu.RLock()
	matched := make([]Link, 0, 2)
	for _, l := range e.in {
		if Matches(l.Topic, topic) {
			matched = append(matched, l)
		}
	}
	e.mu.RUnlock()

	for _, l := range matched {
		e.applyIn(ctx, l, payload)
	}
}

// applyIn проводит одну связку направления In.
func (e *Engine) applyIn(ctx context.Context, l Link, payload []byte) {
	e.count(l.ID, func(s *Stats) { s.Matched++ })

	wire, err := l.ToWire(payload)
	if err != nil {
		e.fail(l, err, "преобразование значения")
		return
	}
	if wire.Clamped {
		e.log.Warn("значение обрезано под диапазон протокола",
			"link", l.Title(), "value", wire.Number,
			"hint", "задайте множитель или выберите текстовую форму")
	}

	// Дедупликация: по таймеру устройства шлют одно и то же, и без неё в
	// умный дом каждую секунду уходили бы одинаковые значения.
	key := hex.EncodeToString(wire.Bytes)
	if l.OnlyChanged {
		e.mu.RLock()
		same := e.last[l.ID] == key
		e.mu.RUnlock()
		if same {
			e.count(l.ID, func(s *Stats) { s.Skipped++ })
			return
		}
	}

	value, err := shclient.Raw(l.TargetID, l.TargetSubID, wire.Bytes)
	if err != nil {
		e.fail(l, err, "сборка пакета")
		return
	}

	// Запись помечается до отправки: событие с сервера может обогнать
	// возврат из Send, и тогда отметка опоздала бы, а петля успела начаться.
	e.markOwn(l.Addr(), key)

	if err := e.sh.Send(ctx, value); err != nil {
		e.fail(l, err, "отправка в умный дом")
		return
	}

	e.mu.Lock()
	e.last[l.ID] = key
	e.mu.Unlock()

	e.count(l.ID, func(s *Stats) {
		s.Delivered++
		s.LastValue = wire.Text
		s.LastAt = time.Now()
		s.LastError = ""
	})
	e.log.Debug("значение доставлено в умный дом",
		"link", l.Title(), "value", wire.Text, "bytes", wire.Hex())
}

// OnEvent разбирает изменение элемента умного дома и публикует команды.
func (e *Engine) OnEvent(_ context.Context, ev Event) {
	// Снимок состояний — не изменение. Транслировать его наружу означало бы
	// при каждом подключении щёлкнуть всеми реле на объекте.
	if ev.Sync {
		return
	}

	addr := ev.Addr()

	e.mu.RLock()
	links := e.out[addr]
	own, hasOwn := e.echo[addr]
	e.mu.RUnlock()

	if len(links) == 0 {
		return
	}

	// Эхо собственной записи: сервер вернул то же, что мы только что послали.
	if hasOwn && own.payload == hex.EncodeToString(ev.Payload) &&
		time.Since(own.at) < echoWindow {
		for _, l := range links {
			e.count(l.ID, func(s *Stats) { s.Echoes++ })
		}
		e.log.Debug("событие признано эхом собственной записи", "addr", addr)
		return
	}

	for _, l := range links {
		e.applyOut(l, ev)
	}
}

// applyOut проводит одну связку направления Out.
func (e *Engine) applyOut(l Link, ev Event) {
	e.count(l.ID, func(s *Stats) { s.Matched++ })

	payload, err := l.ToPayload(ev.Payload)
	if err != nil {
		e.fail(l, err, "чтение значения элемента")
		return
	}

	if err := e.mq.Publish(l.Topic, []byte(payload), l.QoS, l.Retain); err != nil {
		e.fail(l, err, "публикация на шину")
		return
	}

	e.count(l.ID, func(s *Stats) {
		s.Delivered++
		s.LastValue = payload
		s.LastAt = time.Now()
		s.LastError = ""
	})
	e.log.Debug("команда опубликована", "link", l.Title(), "payload", payload)
}

// markOwn запоминает собственную запись в элемент.
func (e *Engine) markOwn(addr, payload string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.echo[addr] = echoEntry{payload: payload, at: time.Now()}
}

// count обновляет счётчики связки.
func (e *Engine) count(id int64, f func(*Stats)) {
	e.mu.Lock()
	defer e.mu.Unlock()

	s, ok := e.stats[id]
	if !ok {
		s = &Stats{}
		e.stats[id] = s
	}
	f(s)
}

// fail записывает ошибку связки и в счётчики, и в журнал.
func (e *Engine) fail(l Link, err error, what string) {
	e.count(l.ID, func(s *Stats) {
		s.Errors++
		s.LastError = what + ": " + err.Error()
	})
	e.log.Error(what, "link", l.Title(), "err", err)
}
