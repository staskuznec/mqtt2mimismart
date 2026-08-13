// Package app собирает демон из частей и управляет их жизнью.
//
// Частей четыре: база, соединение с брокером, соединение с умным домом и
// веб-интерфейс. Веб поднимается всегда, даже когда настроек ещё нет, —
// иначе их негде было бы ввести. Клиенты поднимаются, только когда настройки
// заполнены, и падение любого из них не роняет остальных.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/staskuznec/mqtt2mimismart/internal/link"
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
	db      *store.Store
	version string
	addr    string

	mqtt   *mqtt.Client
	shs    *shs.Client
	engine *link.Engine
	update *update.Checker

	// Разобранный logic.xml. Приезжает в рукопожатии и меняется редко, а
	// разбирать три сотни элементов на каждой отрисовке страницы незачем.
	logicMu    sync.Mutex
	logicHouse logic.House
	logicSize  int
}

// Elements отдаёт элементы умного дома из logic.xml.
//
// Описание приходит от сервера при подключении, поэтому отдельный путь к файлу
// настраивать не нужно: список всегда соответствует тому серверу, с которым мы
// на самом деле работаем.
func (a *App) Elements() []logic.Element {
	if a.shs == nil {
		return nil
	}
	raw := a.shs.Logic()
	if len(raw) == 0 {
		return nil
	}

	a.logicMu.Lock()
	defer a.logicMu.Unlock()

	// Пересобираем, только когда описание сменилось: длина меняется при любой
	// правке логики, а сравнивать целиком дороже, чем разбирать.
	if a.logicSize != len(raw) {
		house, err := logic.Parse(raw)
		if err != nil {
			a.log.Error("разбор logic.xml", "err", err)
			return a.logicHouse.Elements
		}
		a.logicHouse, a.logicSize = house, len(raw)
		a.log.Info("логика умного дома разобрана",
			"элементов", len(house.Elements), "областей", len(house.Areas()))
	}
	return a.logicHouse.Elements
}

// New собирает демон. Клиенты создаются только при заполненных настройках:
// подключаться, не зная куда, всё равно некуда.
func New(log *slog.Logger, db *store.Store, version, addr string) (*App, error) {
	a := &App{log: log, db: db, version: version, addr: addr, update: update.New(version)}

	cfg, err := db.Config(context.Background())
	if err != nil {
		return nil, err
	}
	if !cfg.Ready() {
		log.Warn("настройки не заполнены, откройте веб-интерфейс и завершите первый запуск",
			"url", "http://"+addr)
		return a, nil
	}

	a.shs = shs.New(shs.Config{
		Addr: cfg.SHSAddr,
		Key:  cfg.SHSKey,
		Mac:  cfg.SHSMac,
	}, log.With("component", "shs"))

	a.mqtt, err = mqtt.New(mqtt.Config{
		Addr:     cfg.MQTTAddr,
		User:     cfg.MQTTUser,
		Password: cfg.MQTTPassword,
		ClientID: cfg.MQTTClientID,
	}, log.With("component", "mqtt"))
	if err != nil {
		return nil, err
	}

	a.engine = link.NewEngine(a.shs, a.mqtt, log.With("component", "engine"))
	if err := a.ReloadLinks(context.Background()); err != nil {
		return nil, err
	}
	return a, nil
}

// ReloadLinks перечитывает связки из базы и отдаёт их движку.
//
// Вызывается при запуске и после каждой правки в вебе: перезапускать демон
// ради изменённой связки было бы дико, а держать связки в двух местах — базе
// и памяти движка — значит однажды их рассинхронизировать.
func (a *App) ReloadLinks(ctx context.Context) error {
	if a.engine == nil {
		return nil
	}

	links, err := a.db.Links(ctx)
	if err != nil {
		return err
	}
	a.engine.SetLinks(links)

	enabled := 0
	for _, l := range links {
		if l.Enabled {
			enabled++
		}
	}
	a.log.Info("связки загружены", "всего", len(links), "включено", enabled)
	return nil
}

// Run поднимает всё и держит до отмены контекста.
func (a *App) Run(ctx context.Context) error {
	// Отмена по любой причине гасит остальные части, иначе процесс завис бы
	// на одной живой горутине после падения соседней.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg      sync.WaitGroup
		errOnce sync.Once
		runErr  error
	)
	fail := func(err error) {
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		errOnce.Do(func() { runErr = err })
		cancel()
	}

	if a.shs != nil {
		wg.Add(2)
		go func() {
			defer wg.Done()
			fail(a.shs.Run(ctx))
		}()
		go func() {
			defer wg.Done()
			a.consumeEvents(ctx)
		}()
	}

	if a.mqtt != nil {
		wg.Add(2)
		go func() {
			defer wg.Done()
			fail(a.mqtt.Run(ctx))
		}()
		go func() {
			defer wg.Done()
			a.consumeMessages(ctx)
		}()

		// Режим обучения: подписка на всю шину наполняет снифер, из которого
		// в вебе выбираются топики для связок.
		if err := a.mqtt.Subscribe(mqtt.LearningFilter, 0); err != nil {
			a.log.Error("подписка на шину", "err", err)
		}
	}

	// Проверка обновлений живёт отдельно от всего остального: интернета на
	// объекте может не быть вовсе, и её неудача ничего не роняет.
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.update.Run(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		fail(a.serve(ctx))
	}()

	wg.Wait()
	if runErr != nil {
		return runErr
	}
	return ctx.Err()
}

// eventSummaryPeriod — как часто в журнал уходит сводка по событиям.
//
// Каждое событие писать нельзя: сервер отдаёт снимок всего дома каждые
// несколько секунд, и на объекте в три сотни элементов это десятки строк в
// секунду. Подробности остаются на уровне debug, а на обычном уровне —
// одна строка со счётчиками.
const eventSummaryPeriod = time.Minute

// consumeEvents проводит события из умного дома через движок связок.
func (a *App) consumeEvents(ctx context.Context) {
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

		case e, ok := <-a.shs.Events():
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

			a.engine.OnEvent(ctx, link.Event{
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
func (a *App) consumeMessages(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-a.mqtt.Messages():
			if !ok {
				return
			}
			a.log.Debug("сообщение с шины",
				"topic", m.Topic,
				"payload", string(m.Payload),
				"retained", m.Retained)

			a.engine.OnMessage(ctx, m.Topic, m.Payload)
		}
	}
}

// serve поднимает веб-интерфейс и держит его до отмены контекста.
func (a *App) serve(ctx context.Context) error {
	status := web.Status{}
	if a.shs != nil {
		status.SHS = a.shs.Status
	}
	if a.mqtt != nil {
		status.MQTT = a.mqtt.Status
		status.Topics = a.mqtt.Topics
	}
	if a.engine != nil {
		status.Links = a.engine.Stats
	}
	status.Elements = a.Elements
	status.Reload = a.ReloadLinks
	status.Update = a.update.Info

	srv := &http.Server{
		Addr:    a.addr,
		Handler: web.Handler(a.log, a.db, a.version, status),

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
