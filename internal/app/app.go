// Package app собирает демон из частей и управляет их жизнью.
//
// Частей четыре: база, соединение с брокером, соединение с умным домом и
// веб-интерфейс. Веб поднимается всегда, даже когда настроек ещё нет, —
// иначе их негде было бы ввести. Клиенты поднимаются, только когда настройки
// заполнены, и падение любого из них не роняет остальных.
package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/staskuznec/mqtt2mimismart/internal/link"
	"github.com/staskuznec/mqtt2mimismart/internal/logging"
	"github.com/staskuznec/mqtt2mimismart/internal/logic"
	"github.com/staskuznec/mqtt2mimismart/internal/mqtt"
	"github.com/staskuznec/mqtt2mimismart/internal/shs"
	"github.com/staskuznec/mqtt2mimismart/internal/store"
	"github.com/staskuznec/mqtt2mimismart/internal/update"
	"github.com/staskuznec/mqtt2mimismart/internal/web"
)

// shutdownTimeout — сколько даём веб-серверу доработать начатые запросы.
// Дольше ждать незачем: systemd всё равно прибьёт процесс по своему таймауту.
const shutdownTimeout = 10 * time.Second

// App — собранный демон.
type App struct {
	log     *slog.Logger
	ring    *logging.Ring // последние записи журнала, для показа в вебе
	db      *store.Store
	version string
	addr    string
	base    string // подкаталог снаружи, когда шлюз стоит за веб-сервером

	update *update.Checker

	// Клиенты пересоздаются при каждой правке настроек, а веб читает их
	// состояние одновременно — поэтому только под мьютексом.
	mu          sync.Mutex
	mqtt        *mqtt.Client
	shs         *shs.Client
	engine      *link.Engine
	stopSession context.CancelFunc

	// Разобранный logic.xml. Приезжает в рукопожатии и меняется редко, а
	// разбирать три сотни элементов на каждой отрисовке страницы незачем.
	logicMu    sync.Mutex
	logicHouse logic.House
	logicSum   [32]byte        // отпечаток описания, по которому виден его пересмотр
	logicAddrs map[string]bool // адреса из описания: движок сверяется на каждом сообщении
}

// Elements отдаёт элементы умного дома из logic.xml.
//
// Описание приходит от сервера при подключении, поэтому отдельный путь к файлу
// настраивать не нужно: список всегда соответствует тому серверу, с которым мы
// на самом деле работаем.
func (a *App) Elements() []logic.Element {
	client, _, _ := a.clients()
	if client == nil {
		return nil
	}
	raw := client.Logic()
	if len(raw) == 0 {
		return nil
	}

	a.logicMu.Lock()
	defer a.logicMu.Unlock()

	// Пересобираем, только когда описание сменилось.
	//
	// Отпечаток, а не длина: правка адреса длину не меняет вовсе — 563:160 и
	// 563:116 одинаковы по числу байт, — и элемент оставался в списке со
	// старым адресом сколько угодно долго, сколько ни жми «перечитать».
	// Хеш пары сотен килобайт стоит доли миллисекунды, разбор XML — дороже.
	sum := sha256.Sum256(raw)
	if sum != a.logicSum {
		house, err := logic.Parse(raw)
		if err != nil {
			a.log.Error("разбор logic.xml", "err", err)
			return a.logicHouse.Elements
		}
		a.logicHouse, a.logicSum = house, sum
		a.logicAddrs = make(map[string]bool, len(house.Elements))
		for _, e := range house.Elements {
			a.logicAddrs[e.Addr()] = true
		}
		a.log.Info("логика умного дома разобрана",
			"элементов", len(house.Elements), "областей", len(house.Areas()))
	}
	return a.logicHouse.Elements
}

// knownAddr сообщает, есть ли элемент с таким адресом в описании дома.
//
// Пока описание не приехало, отвечаем «есть»: связь с умным домом могла
// оборваться, и запрещать работу на этом основании — значит останавливать
// исправный объект.
func (a *App) knownAddr(addr string) bool {
	// Обращение к Elements держит разбор свежим: описание перечитывается по
	// кнопке и при переподключении, а набор адресов собирается там же.
	if len(a.Elements()) == 0 {
		return true
	}

	a.logicMu.Lock()
	defer a.logicMu.Unlock()
	return a.logicAddrs[addr]
}

// New собирает демон. Клиенты создаются только при заполненных настройках:
// подключаться, не зная куда, всё равно некуда.
func New(log *slog.Logger, ring *logging.Ring, db *store.Store,
	version, addr, basePath string) (*App, error) {

	a := &App{
		log: log, ring: ring, db: db, version: version, addr: addr, base: basePath,
		update: update.New(version, log.With("component", "update")),
	}

	return a, nil
}

// connect создаёт клиентов по текущим настройкам.
//
// Отдельно от New, потому что вызывается ещё и после правки настроек в вебе:
// требовать перезапуск службы ради введённого пароля — значит гнать человека
// в консоль оттуда, где он только что всё настроил.
func (a *App) connect(ctx context.Context) error {
	cfg, err := a.db.Config(ctx)
	if err != nil {
		return err
	}
	if !cfg.Ready() {
		a.log.Warn("настройки не заполнены, откройте веб-интерфейс и завершите первый запуск",
			"url", "http://"+a.addr)
		return nil
	}

	shsClient := shs.New(shs.Config{
		Addr: cfg.SHSAddr,
		Key:  cfg.SHSKey,
		Mac:  cfg.SHSMac,
	}, a.log.With("component", "shs"))

	mqttClient, err := mqtt.New(mqtt.Config{
		Addr:     cfg.MQTTAddr,
		User:     cfg.MQTTUser,
		Password: cfg.MQTTPassword,
		ClientID: cfg.MQTTClientID,
	}, a.log.With("component", "mqtt"))
	if err != nil {
		return err
	}

	engine := link.NewEngine(shsClient, mqttClient, a.log.With("component", "engine"))

	// Движок обязан знать, какие адреса вообще существуют: связка переживает
	// правку logic.xml, и после неё пишет в никуда — или, хуже, в чужой
	// элемент, заведённый по освободившемуся адресу.
	engine.SetKnownAddrs(a.knownAddr)

	a.mu.Lock()
	a.shs, a.mqtt = shsClient, mqttClient
	a.engine = engine
	a.mu.Unlock()

	return a.ReloadLinks(ctx)
}

// Снимки состояния для веба. Через методы, а не напрямую полями: клиенты
// пересоздаются при правке настроек, и захваченная один раз ссылка показывала
// бы прошлое соединение — или пустоту, если при старте настроек ещё не было.

func (a *App) clients() (*shs.Client, *mqtt.Client, *link.Engine) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.shs, a.mqtt, a.engine
}

func (a *App) shsStatus() shs.Status {
	c, _, _ := a.clients()
	if c == nil {
		return shs.Status{Phase: shs.PhaseDisconnected}
	}
	return c.Status()
}

func (a *App) mqttStatus() mqtt.Status {
	_, c, _ := a.clients()
	if c == nil {
		return mqtt.Status{}
	}
	return c.Status()
}

func (a *App) topics() []mqtt.TopicInfo {
	_, c, _ := a.clients()
	if c == nil {
		return nil
	}
	return c.Topics()
}

func (a *App) linkStats() map[int64]link.Stats {
	_, _, e := a.clients()
	if e == nil {
		return nil
	}
	return e.Stats()
}

// ReloadLinks перечитывает связки из базы и отдаёт их движку.
//
// Вызывается при запуске и после каждой правки в вебе: перезапускать демон
// ради изменённой связки было бы дико, а держать связки в двух местах — базе
// и памяти движка — значит однажды их рассинхронизировать.
func (a *App) ReloadLinks(ctx context.Context) error {
	_, mqttClient, engine := a.clients()
	if engine == nil {
		return nil
	}

	links, err := a.db.Links(ctx)
	if err != nil {
		return err
	}
	engine.SetLinks(links)

	// Повторная подписка на ту же маску заставляет брокер прислать заново всё,
	// что лежит у него с retain.
	//
	// Без этого только что заведённая связка стоит пустой до следующей
	// публикации: сохранённые значения брокер отдаёт один раз, в момент
	// подписки, — то есть до того, как связку завели. У редких топиков это
	// заметно особенно: доступность инвертора или его серийный номер меняются
	// раз в жизни, и ждать пришлось бы до ближайшего переиздания.
	if mqttClient != nil {
		if err := mqttClient.Subscribe(mqtt.LearningFilter, 0); err != nil {
			a.log.Error("запрос сохранённых значений", "err", err)
		}
	}

	enabled := 0
	for _, l := range links {
		if l.Enabled {
			enabled++
		}
	}
	a.log.Info("связки загружены", "всего", len(links), "включено", enabled)
	return nil
}

// ReloadLogic просит умный дом прислать описание дома заново.
//
// Описание приезжает только в рукопожатии, поэтому «перечитать» — это
// поздороваться заново. Рвём одно соединение из двух: MQTT не при чём, и
// дёргать шину ради списка элементов незачем.
func (a *App) ReloadLogic() error {
	shsClient, _, _ := a.clients()
	if shsClient == nil {
		return errors.New("умный дом не настроен")
	}
	return shsClient.Reconnect()
}

// Run поднимает всё и держит до отмены контекста.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var runErr error

	// Веб живёт отдельно от соединений и переживает их пересоздание: настройки
	// правят именно в нём, и он не должен исчезать в момент применения.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := a.serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
			runErr = err
			cancel()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		a.update.Run(ctx)
	}()

	a.runConnections(ctx)

	wg.Wait()
	if runErr != nil {
		return runErr
	}
	return ctx.Err()
}

// runConnections держит соединения и поднимает их заново после правки
// настроек, пока не отменён общий контекст.
func (a *App) runConnections(ctx context.Context) {
	for {
		sessionCtx, stop := context.WithCancel(ctx)

		a.mu.Lock()
		a.stopSession = stop
		a.mu.Unlock()

		if err := a.connect(sessionCtx); err != nil {
			a.log.Error("не удалось создать соединения", "err", err)
		}

		shsClient, mqttClient, engine := a.clients()

		var wg sync.WaitGroup
		if shsClient != nil {
			wg.Add(2)
			go func() { defer wg.Done(); _ = shsClient.Run(sessionCtx) }()
			go func() { defer wg.Done(); a.consumeEvents(sessionCtx, shsClient, engine) }()
		}
		if mqttClient != nil {
			wg.Add(2)
			go func() { defer wg.Done(); _ = mqttClient.Run(sessionCtx) }()
			go func() { defer wg.Done(); a.consumeMessages(sessionCtx, mqttClient, engine) }()

			// Режим обучения: подписка на всю шину наполняет снифер, из
			// которого в вебе выбираются топики для связок.
			if err := mqttClient.Subscribe(mqtt.LearningFilter, 0); err != nil {
				a.log.Error("подписка на шину", "err", err)
			}
		}

		<-sessionCtx.Done()
		wg.Wait()
		stop()

		// Отменён общий контекст — это остановка демона, а не применение
		// настроек: выходим.
		if ctx.Err() != nil {
			return
		}
		a.log.Info("настройки изменились, поднимаем соединения заново")
	}
}

// Reconfigure заново поднимает соединения по текущим настройкам.
// Вызывается вебом после сохранения настроек.
func (a *App) Reconfigure() {
	a.mu.Lock()
	stop := a.stopSession
	a.mu.Unlock()

	if stop != nil {
		stop()
	}
}

// Restart завершает процесс, чтобы systemd поднял его заново. Нужен после
// обновления: работающий процесс продолжает жить со старым кодом.
func (a *App) Restart() {
	a.log.Info("завершаемся для перезапуска")
	os.Exit(0)
}

// eventSummaryPeriod — как часто в журнал уходит сводка по событиям.
//
// Каждое событие писать нельзя: сервер отдаёт снимок всего дома каждые
// несколько секунд, и на объекте в три сотни элементов это десятки строк в
// секунду. Подробности остаются на уровне debug, а на обычном уровне —
// одна строка со счётчиками.
const eventSummaryPeriod = time.Minute

// consumeEvents проводит события из умного дома через движок связок.
func (a *App) consumeEvents(ctx context.Context, client *shs.Client, engine *link.Engine) {
	var events, syncs uint64
	summary := time.NewTicker(eventSummaryPeriod)
	defer summary.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-summary.C:
			if events > 0 {
				a.log.Info("события из умного дома",
					"за_минуту", events, "из_них_снимок", syncs)
				events, syncs = 0, 0
			}

		case e, ok := <-client.Events():
			if !ok {
				return
			}
			events++
			if e.Sync {
				syncs++
			}
			a.log.Debug("событие из умного дома",
				"addr", e.Addr(),
				"pd", e.PD,
				"payload", fmt.Sprintf("% x", e.Payload),
				"len", len(e.Payload),
				"sync", e.Sync)

			engine.OnEvent(ctx, link.Event{
				ID:      e.ID,
				SubID:   e.SubID,
				Payload: e.Payload,
				Sync:    e.Sync,
			})
		}
	}
}

// consumeMessages проводит сообщения с шины через движок связок.
//
// В снифер они уже осели при приёме, здесь остаётся только раскладывание по
// элементам умного дома.
func (a *App) consumeMessages(ctx context.Context, client *mqtt.Client, engine *link.Engine) {
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-client.Messages():
			if !ok {
				return
			}
			a.log.Debug("сообщение с шины",
				"topic", m.Topic,
				"payload", string(m.Payload),
				"retained", m.Retained)

			engine.OnMessage(ctx, m.Topic, m.Payload)
		}
	}
}

// serve поднимает веб-интерфейс и держит его до отмены контекста.
func (a *App) serve(ctx context.Context) error {
	status := web.Status{
		SHS:    a.shsStatus,
		MQTT:   a.mqttStatus,
		Topics: a.topics,
		Links:  a.linkStats,
	}
	status.Elements = a.Elements
	status.Reload = a.ReloadLinks
	status.Update = a.update.Info
	status.CheckUpdates = func() { a.update.EnsureFresh(context.Background()) }
	status.CheckNow = a.update.Check
	status.Log = a.ring.Entries
	status.Reconfigure = a.Reconfigure
	status.ReloadLogic = a.ReloadLogic
	status.Upgrade = a.update.Apply
	status.Restart = a.Restart

	srv := &http.Server{
		Addr:    a.addr,
		Handler: web.Handler(a.log, a.db, a.version, a.base, status),

		// Голые таймауты обязательны: без них одно зависшее соединение
		// занимает горутину до конца жизни процесса.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		a.log.Info("веб-интерфейс слушает", "addr", a.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("веб-сервер: %w", err)
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	// Свой контекст: тот, что пришёл, уже отменён, и остановка по нему
	// завершилась бы мгновенно, оборвав запросы на середине.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("остановка веб-сервера: %w", err)
	}
	a.log.Info("веб-интерфейс остановлен")
	return nil
}
