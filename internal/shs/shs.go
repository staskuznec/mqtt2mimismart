// Package shs — долгоживущее соединение с сервером умного дома MimiSmart.
//
// Поверх библиотеки github.com/staskuznec/shClientMimismart добавляется то,
// чего в ней намеренно нет: супервизор, который переподключается после обрыва,
// первичная синхронизация состояний и признак «это ещё синхронизация, а не
// живое событие».
//
// Последнее важнее, чем кажется. При каждом подключении мы запрашиваем
// состояния всех элементов, и сервер отдаёт их пачками — теми же пакетами, что
// и обычное нажатие. Если пропустить их дальше как события, шлюз при каждом
// старте опубликует состояние всех ламп командами в MQTT, то есть щёлкнет
// всеми реле на объекте. Поэтому события первичной синхронизации помечаются
// флагом Sync: базовую линию и показания в вебе они обновляют, публикаций не
// порождают.
package shs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	shclient "github.com/staskuznec/shClientMimismart"
)

// Параметры соединения. Подобраны так, чтобы молчаливый обрыв обнаруживался за
// разумное время, а переподключение не превращалось в шторм запросов.
const (
	// pollInterval — период запроса состояний. Он же поддерживает соединение:
	// сервер выкидывает молчащих, а этот запрос как раз и подаёт признак жизни.
	//
	// Опрос ведём сами, а не через keepalive библиотеки, ровно по одной
	// причине: нам нужно знать момент запроса. Сервер отвечает на него полным
	// снимком состояний, и без этого знания снимок неотличим от настоящих
	// изменений — а значит уехал бы наружу командами по всем элементам.
	pollInterval = shclient.ReferenceKeepalive

	// idleTimeout — сколько ждём пакетов, прежде чем считать связь оборванной.
	// Втрое больше периода опроса: одиночная потеря не должна ронять
	// соединение, а вот три подряд — уже повод переподключиться.
	idleTimeout = 3 * pollInterval

	// syncWindow — сколько после запроса состояний ответы считаются снимком.
	// Заведомо меньше периода опроса, иначе живыми изменениями не считалось бы
	// вообще ничего. На стенде весь снимок дома укладывается в сотню
	// миллисекунд, так что секунды с запасом хватает.
	syncWindow = 1 * time.Second

	// Пауза перед повторным подключением: от секунды до минуты с удвоением.
	// Больше минуты ждать незачем — сервер обычно поднимается быстрее, а
	// оператор в вебе видит, что связи нет.
	minBackoff = 1 * time.Second
	maxBackoff = 60 * time.Second

	// eventBuffer — запас в очереди событий. Нажатия приходят редко, но при
	// первичной синхронизации сразу прилетает состояние всего дома.
	eventBuffer = 512
)

// Config — параметры подключения. Приходят из базы, заполняются в вебе.
type Config struct {
	Addr string // host:port сервера умного дома
	Key  string // ключ AES: 16, 24 или 32 байта
	Mac  string // mac-id клиента; пусто — сгенерируется случайный
}

// Event — событие от сервера умного дома.
type Event struct {
	ID      uint16    // адрес элемента
	SubID   uint8     //
	PD      uint8     // код команды: 7 — состояние элемента, 15 — из ответа модуля
	Payload []byte    // полезная нагрузка как есть; что она значит, решает тип элемента
	At      time.Time // когда пакет вычитан из сокета

	// Sync сообщает, что событие пришло в ответ на первичный запрос состояний
	// и является частью снимка, а не изменением. Такие события обновляют
	// базовую линию, но наружу — в MQTT — уходить не должны.
	Sync bool
}

// Addr возвращает адрес элемента в виде "id:subid".
func (e Event) Addr() string { return fmt.Sprintf("%d:%d", e.ID, e.SubID) }

// Phase — состояние соединения.
type Phase string

const (
	PhaseDisconnected Phase = "disconnected"
	PhaseConnecting   Phase = "connecting"
	PhaseSyncing      Phase = "syncing"
	PhaseConnected    Phase = "connected"
)

// Status — снимок состояния для веб-интерфейса.
type Status struct {
	Phase       Phase
	ClientID    uint16
	Since       time.Time // когда установлено текущее соединение
	LastError   string
	LastEventAt time.Time
	Events      uint64 // сколько событий принято за всё время
	Sent        uint64 // сколько значений отправлено
	Connects    uint64 // сколько раз соединение поднималось, включая первый
	LogicBytes  int    // размер полученного logic.xml
}

// Client — соединение с сервером умного дома, живущее всё время работы шлюза.
type Client struct {
	log    *slog.Logger
	events chan Event

	mu       sync.Mutex
	cfg      Config
	client   *shclient.Client
	logic    []byte
	status   Status
	syncTill time.Time
}

// New создаёт клиента. Соединение не открывается: этим занимается Run.
func New(cfg Config, log *slog.Logger) *Client {
	return &Client{
		log:    log,
		cfg:    cfg,
		events: make(chan Event, eventBuffer),
		status: Status{Phase: PhaseDisconnected},
	}
}

// Events отдаёт канал событий. Читать его обязан ровно один потребитель:
// событие достаётся тому, кто успел первым.
func (c *Client) Events() <-chan Event { return c.events }

// Status возвращает снимок состояния.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Logic возвращает logic.xml, полученный при последнем рукопожатии.
// Пусто, если соединение ещё не поднималось.
func (c *Client) Logic() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logic
}

// Send отправляет значения в умный дом. Если соединения нет, возвращает
// ошибку: молча терять команду хуже, чем сказать о ней вызывающему.
func (c *Client) Send(ctx context.Context, values ...shclient.Value) error {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	if client == nil {
		return shclient.ErrNotConnected
	}
	if err := client.Send(ctx, values...); err != nil {
		return err
	}

	c.mu.Lock()
	c.status.Sent += uint64(len(values))
	c.mu.Unlock()
	return nil
}

// Run держит соединение до отмены контекста, переподключаясь после обрыва.
// Возвращает управление только когда контекст отменён.
func (c *Client) Run(ctx context.Context) error {
	defer close(c.events)

	backoff := minBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := c.session(ctx)
		switch {
		case ctx.Err() != nil:
			// Отмена — штатное завершение, а не ошибка соединения.
			return ctx.Err()
		case err != nil:
			c.setError(err)
			c.log.Error("соединение с умным домом разорвано",
				"err", err, "retry_in", backoff.String())
		default:
			c.log.Warn("цикл чтения завершился без ошибки, переподключаемся")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		// Удвоение с потолком: при недоступном сервере не долбимся раз в секунду.
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// session проживает одно соединение: подключение, первичная синхронизация и
// чтение событий до обрыва.
func (c *Client) session(ctx context.Context) error {
	// Свой контекст на сессию: по её завершении гасим и поддержание
	// соединения, и таймер окна синхронизации, чтобы они не пережили
	// соединение, к которому относятся.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	client, logicBuf, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := client.Close(); err != nil {
			c.log.Debug("закрытие соединения с умным домом", "err", err)
		}
		c.mu.Lock()
		c.client = nil
		c.status.Phase = PhaseDisconnected
		c.mu.Unlock()
	}()

	now := time.Now()
	c.mu.Lock()
	c.client = client
	c.logic = logicBuf.Bytes()
	c.status.Phase = PhaseSyncing
	c.status.ClientID = client.ClientID()
	c.status.Since = now
	c.status.LastError = ""
	c.status.LogicBytes = logicBuf.Len()
	// Всё, что придёт в этом окне, — снимок состояния, а не изменения.
	c.syncTill = now.Add(syncWindow)
	c.mu.Unlock()

	c.log.Info("умный дом на связи",
		"client_id", client.ClientID(),
		"logic_bytes", logicBuf.Len())

	// Снимок состояний: без него мы не знаем, в каком положении элементы, и
	// первое же изменение не с чем сравнить.
	if err := c.poll(ctx, client); err != nil {
		return fmt.Errorf("запрос состояний: %w", err)
	}

	go c.pollLoop(ctx, client)

	return client.Listen(ctx, c.onEvent(ctx))
}

// poll запрашивает состояния и открывает окно, в течение которого ответы
// считаются снимком, а не изменениями.
func (c *Client) poll(ctx context.Context, client *shclient.Client) error {
	// Окно открывается до запроса: ответ может прийти быстрее, чем вернётся
	// управление, и тогда начало снимка успело бы уехать как изменения.
	c.mu.Lock()
	c.syncTill = time.Now().Add(syncWindow)
	c.mu.Unlock()

	return client.RequestAll(ctx)
}

// pollLoop опрашивает состояния, пока живо соединение.
func (c *Client) pollLoop(ctx context.Context, client *shclient.Client) {
	// Первый опрос уже сделан вызывающим, поэтому сразу ждём период.
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.status.Phase == PhaseSyncing {
				c.status.Phase = PhaseConnected
			}
			c.mu.Unlock()

			if err := c.poll(ctx, client); err != nil {
				// Не смертельно: соединение всё равно оборвётся по
				// idleTimeout, если сервер перестал отвечать.
				c.log.Warn("запрос состояний не удался", "err", err)
			}
		}
	}
}

// dial поднимает одно соединение.
func (c *Client) dial(ctx context.Context) (*shclient.Client, *bytes.Buffer, error) {
	c.mu.Lock()
	cfg := c.cfg
	c.status.Phase = PhaseConnecting
	c.mu.Unlock()

	// Буфер под logic.xml свой на каждое подключение: иначе при
	// переподключении логика допишется к предыдущей копии.
	logicBuf := &bytes.Buffer{}

	client, err := shclient.New(cfg.Addr, cfg.Key,
		shclient.WithMacID(cfg.Mac),
		shclient.WithLogger(c.log),
		shclient.WithLogicSink(logicBuf),
		// Поддержание соединения библиотекой выключено: этим занят наш
		// собственный опрос, а два источника запросов только мешали бы.
		shclient.WithIdleTimeout(idleTimeout),
		// Вычитывать ответы после отправки не надо: этим занят цикл чтения,
		// и они бы дрались за один сокет.
		shclient.WithAutoDrain(false),
	)
	if err != nil {
		return nil, nil, err
	}

	if err := client.Connect(ctx); err != nil {
		return nil, nil, err
	}

	c.mu.Lock()
	c.status.Connects++
	c.mu.Unlock()

	return client, logicBuf, nil
}

// onEvent собирает обработчик событий библиотеки.
//
// Обработчик вызывается синхронно из цикла чтения: пока он работает, пакеты не
// читаются. Поэтому здесь только перекладывание в очередь, вся обработка —
// у потребителя.
func (c *Client) onEvent(ctx context.Context) shclient.Handler {
	return func(e shclient.Event) {
		c.mu.Lock()
		sync := e.At.Before(c.syncTill)
		c.status.Events++
		c.status.LastEventAt = e.At
		c.mu.Unlock()

		event := Event{
			ID:      e.SenderID,
			SubID:   e.SenderSubID,
			PD:      e.PD,
			Payload: e.Payload,
			At:      e.At,
			Sync:    sync,
		}

		select {
		case c.events <- event:
			return
		default:
		}

		// Очередь заполнена: потребитель не справляется. Дальше мы блокируемся,
		// а значит приостанавливаем чтение из сокета — это лучше, чем выбросить
		// событие. Потерянное состояние элемента выглядит как «шлюз иногда не
		// срабатывает» и ищется потом неделями, а приостановка приёма честно
		// упирается в TCP и видна в журнале.
		c.log.Warn("очередь событий заполнена, приём приостановлен",
			"addr", event.Addr(), "buffer", cap(c.events))
		select {
		case c.events <- event:
		case <-ctx.Done():
		}
	}
}

// setError запоминает ошибку соединения для веб-интерфейса.
func (c *Client) setError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Phase = PhaseDisconnected
	if err != nil && !errors.Is(err, context.Canceled) {
		c.status.LastError = err.Error()
	}
}
