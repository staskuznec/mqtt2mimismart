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

	mu   sync.RWMutex
	in   []Link
	out  map[string][]Link
	echo map[string]echoEntry

	// Последнее доставленное значение по связкам, отдельно на каждое
	// направление: в умный дом уходят байты, на шину — текст команды.
	last    map[int64]string
	lastOut map[int64]string

	stats map[int64]*Stats

	// known сообщает, есть ли элемент с таким адресом в описании дома.
	//
	// Нужна, чтобы не писать в пустоту: элемент можно удалить из logic.xml
	// или перенумеровать, а связка останется — она живёт в базе шлюза, и
	// умный дом о ней не знает. Пакеты при этом уходят исправно, ошибки нет,
	// и заметить это можно только по неработающему показанию. Хуже, если по
	// тому же адресу заведут другой элемент: туда поедут чужие значения.
	//
	// Пусто — проверять нечем: описание ещё не приехало, и запрещать работу
	// на этом основании нельзя.
	known func(addr string) bool

	// reported — по каким связкам об отсутствии элемента уже сказано.
	// Иначе строка про один и тот же адрес уходила бы в журнал на каждом
	// сообщении с шины.
	reported map[int64]bool
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
		log:      log,
		sh:       sh,
		mq:       mq,
		out:      make(map[string][]Link),
		last:     make(map[int64]string),
		lastOut:  make(map[int64]string),
		echo:     make(map[string]echoEntry),
		stats:    make(map[int64]*Stats),
		reported: make(map[int64]bool),
	}
}

// SetKnownAddrs задаёт проверку адреса по описанию дома.
//
// Передавать функцию, а не готовый набор: logic.xml перечитывается по кнопке
// и меняется при переподключении, и движок должен видеть свежее описание, а
// не снимок, сделанный при запуске.
func (e *Engine) SetKnownAddrs(known func(addr string) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.known = known
	e.reported = make(map[int64]bool)
}

// elementExists проверяет, есть ли элемент связки в описании дома.
func (e *Engine) elementExists(l Link) bool {
	e.mu.RLock()
	known := e.known
	e.mu.RUnlock()

	return known == nil || known(l.Addr())
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
	for id := range e.lastOut {
		if _, ok := alive[id]; !ok {
			delete(e.lastOut, id)
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

	// Элемента может не быть вовсе: связка живёт в базе шлюза и переживает
	// правку logic.xml. Отправлять в такой адрес незачем — в лучшем случае
	// пакет пропадёт, в худшем по этому адресу успели завести другой элемент.
	if !e.elementExists(l) {
		e.mu.Lock()
		first := !e.reported[l.ID]
		e.reported[l.ID] = true
		e.mu.Unlock()

		if first {
			e.log.Warn("элемента нет в описании дома, связка ничего не пишет",
				"link", l.Title(), "addr", l.Addr(),
				"hint", "проверьте logic.xml или переназначьте элемент в устройстве")
		}
		e.count(l.ID, func(s *Stats) {
			s.Errors++
			s.LastError = "элемента " + l.Addr() + " нет в logic.xml"
		})
		return
	}

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

	value, err := l.Value(ev.Payload)
	if err != nil {
		e.fail(l, err, "чтение значения элемента")
		return
	}
	payload := l.MapValue(value)

	// Состояния элементов приезжают снова и снова — в каждом ответе на запрос
	// состояний. Без отсева повторов шлюз слал бы команду по каждой лампе на
	// каждом опросе, то есть несколько раз в минуту без всякой причины.
	//
	// Нажатие — другое дело: нажали дважды, значит переключить надо дважды,
	// поэтому мгновенные действия проходят всегда.
	if l.OnlyChanged && !Momentary(value) {
		e.mu.RLock()
		same := e.lastOut[l.ID] == payload
		e.mu.RUnlock()
		if same {
			e.count(l.ID, func(s *Stats) { s.Skipped++ })
			return
		}
	}

	if err := e.mq.Publish(l.Topic, []byte(payload), l.QoS, l.Retain); err != nil {
		e.fail(l, err, "публикация на шину")
		return
	}

	e.mu.Lock()
	e.lastOut[l.ID] = payload
	e.mu.Unlock()

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
