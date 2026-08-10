// Бинарник управления реле Shelly 2.5 из умного дома MimiSmart.
//
// Умный дом запускает его и передаёт команду HEX-строкой в одном из
// аргументов. После декодирования получается строка вида:
//
//	192.168.20.222|on|0                                  — включить канал 0
//	192.168.20.222|off|1                                 — выключить канал 1
//	192.168.20.222|toggle|0                              — переключить канал 0
//	192.168.20.222|status|0|state=563:111|power=563:113  — опросить и отдать
//	shadow:secret@192.168.20.222|status|0|state=563:111  — с Basic Auth
//	192.168.20.222|status|                               — все каналы по конфигу
//
// Адреса назначения передаются прямо в команде парами "цель=id:subid" — тогда
// править конфиг при добавлении реле не нужно. Если адресов нет, привязка
// берётся из shelly.yaml по IP устройства.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog/log"

	"Shelly_2.5/internal/command"
	"Shelly_2.5/internal/config"
	"Shelly_2.5/internal/logger"
	"Shelly_2.5/internal/reporter"
	"Shelly_2.5/internal/shelly"
	"Shelly_2.5/internal/statecache"
)

const (
	defaultRunTimeout  = 30 * time.Second
	defaultHTTPTimeout = 5 * time.Second

	exitUsage   = 2
	exitFailure = 1
	exitTimeout = 124

	// Счётчик энергии Shelly считает в ватт-минутах.
	wattMinutesPerKWh = 60000
)

// configPath — путь к прочитанному конфигу. Рядом с ним лежит кэш
// отправленных значений.
var configPath string

func main() {
	if len(os.Args) < 2 {
		_, _ = fmt.Fprintf(os.Stderr,
			"Usage: %s [args...] <command_hex> [args...]\n"+
				"  command_hex — HEX от строки \"host|action|channel[|цель=id:subid]...\"\n"+
				"  action      — on | off | toggle | status\n"+
				"  цели        — state, power, energy, temp, overpower, overtemp\n"+
				"  Позиция аргумента с командой определяется автоматически;\n"+
				"  закрепить её можно переменной %s.\n",
			os.Args[0], command.EnvArgIndex)
		os.Exit(exitUsage)
	}

	cmd, argIndex, err := command.FromArgs(os.Args)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(exitUsage)
	}

	cfg, cfgPath, err := config.Load()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(exitFailure)
	}
	configPath = cfgPath

	logger.Init(cfg.Logging)
	log.Debug().
		Str("config", cfgPath).
		Int("arg_index", argIndex).
		Str("ip", cmd.IP).
		Str("action", string(cmd.Action)).
		Int("channel", cmd.Channel).
		Int("targets", len(cmd.Targets)).
		Msg("Command parsed")

	// Жёсткий watchdog: даже если сеть подвисла на I/O, процесс не останется висеть.
	timeout := defaultRunTimeout
	if cfg.Runtime.TimeoutSec > 0 {
		timeout = time.Duration(cfg.Runtime.TimeoutSec) * time.Second
	}
	go func() {
		<-time.After(timeout)
		log.Error().Dur("timeout", timeout).Msg("Process timeout exceeded, exiting")
		os.Exit(exitTimeout)
	}()

	if err := run(cfg, cmd); err != nil {
		log.Error().Err(err).Msg("Command failed")
		os.Exit(exitFailure)
	}
}

// run выполняет команду и, если нужно, отдаёт состояние в умный дом.
func run(cfg *config.Config, cmd *command.Command) error {
	// Устройство из конфига нужно только тогда, когда адреса назначения
	// не переданы в команде. Иначе оно опционально — из него берутся
	// разве что учётные данные, если их нет в самой команде.
	device, deviceErr := cfg.FindDevice(cmd.IP)
	if !cmd.HasTargets() && deviceErr != nil {
		return fmt.Errorf("%w (или передайте адреса назначения прямо в команде)", deviceErr)
	}

	client := newClient(cfg, cmd, device)

	httpTimeout := defaultHTTPTimeout
	if cfg.Runtime.HTTPTimeout > 0 {
		httpTimeout = time.Duration(cfg.Runtime.HTTPTimeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*httpTimeout)
	defer cancel()

	if cmd.Action != command.ActionStatus {
		return runAction(ctx, cfg, cmd, device, client)
	}
	return runStatus(ctx, cfg, cmd, device, client)
}

// newClient собирает HTTP-клиент реле, разбираясь с учётными данными:
// приоритет у того, что пришло в команде, конфиг — запасной вариант.
func newClient(cfg *config.Config, cmd *command.Command, device *config.Device) *shelly.Client {
	httpTimeout := defaultHTTPTimeout
	if cfg.Runtime.HTTPTimeout > 0 {
		httpTimeout = time.Duration(cfg.Runtime.HTTPTimeout) * time.Second
	}
	opts := []shelly.Option{shelly.WithTimeout(httpTimeout)}

	switch {
	case cmd.User != "" || cmd.Password != "":
		opts = append(opts, shelly.WithBasicAuth(cmd.User, cmd.Password))
	case device != nil && (device.User != "" || device.Password != ""):
		opts = append(opts, shelly.WithBasicAuth(device.User, device.Password))
	}
	return shelly.New(cmd.IP, opts...)
}

// runAction переключает реле и отдаёт получившееся состояние.
func runAction(ctx context.Context, cfg *config.Config, cmd *command.Command,
	device *config.Device, client *shelly.Client) error {

	state, err := apply(ctx, client, cmd)
	if err != nil {
		return err
	}

	// Ответ на turn=on иногда содержит состояние, каким оно было ДО
	// переключения — тогда в умный дом уехало бы неверное значение и галка
	// в интерфейсе сразу вернулась бы назад. Поэтому перечитываем состояние
	// отдельным запросом и отдаём то, что реле показывает по факту.
	if d := cfg.Runtime.VerifyDelay(); d > 0 {
		if err := sleepCtx(ctx, d); err != nil {
			return err
		}
		fresh, err := client.Relay(ctx, shelly.Channel(cmd.Channel))
		if err != nil {
			log.Warn().Err(err).
				Str("ip", cmd.IP).
				Int("channel", cmd.Channel).
				Msg("Не удалось перечитать состояние, отдаём ответ команды")
		} else {
			if fresh.IsOn != state.IsOn {
				log.Info().
					Bool("from_command", state.IsOn).
					Bool("verified", fresh.IsOn).
					Msg("Состояние в ответе команды разошлось с фактическим")
			}
			state = fresh
		}
	}

	log.Info().
		Str("ip", cmd.IP).
		Int("channel", cmd.Channel).
		Str("action", string(cmd.Action)).
		Bool("ison", state.IsOn).
		Msg("Relay switched")

	if !cfg.Runtime.ReportAfterAction() {
		return nil
	}

	// Адрес состояния из команды имеет приоритет над конфигом.
	if addr, ok := cmd.ChannelTarget(command.TargetState, cmd.Channel); ok {
		return report(ctx, cfg, []reporter.Reading{{
			Point: targetPoint(command.TargetState, addr),
			Value: reporter.BoolValue(state.IsOn),
			Label: commandLabel(cmd),
		}})
	}
	if device == nil {
		return nil // некуда отдавать состояние — команда всё равно выполнена
	}

	ch, err := device.FindChannel(cmd.Channel)
	if err != nil {
		return err
	}
	return report(ctx, cfg, []reporter.Reading{{
		Point: ch.State.WithByteDefault(),
		Value: reporter.BoolValue(state.IsOn),
		Label: channelLabel(device, ch),
	}})
}

// runStatus опрашивает устройство и раскладывает ответ по адресам.
func runStatus(ctx context.Context, cfg *config.Config, cmd *command.Command,
	device *config.Device, client *shelly.Client) error {

	// Один запрос /status отдаёт оба реле, мощность и температуру.
	status, err := client.Status(ctx)
	if err != nil {
		return err
	}

	var readings []reporter.Reading
	if cmd.HasTargets() {
		readings, err = collectTargets(cmd, status)
	} else {
		readings, err = collect(device, cmd, status)
	}
	if err != nil {
		return err
	}
	return report(ctx, cfg, readings)
}

// report отправляет значения в умный дом либо, в сухом прогоне, печатает их.
//
// Перед отправкой значения прогоняются через кэш: то, что уже отправлено в
// этот элемент и с тех пор не изменилось, повторно не шлётся. Иначе по
// таймеру каждую минуту уходили бы одни и те же числа, и обработчик лампы
// в скрипте дёргался бы вхолостую.
func report(ctx context.Context, cfg *config.Config, readings []reporter.Reading) error {
	if cfg.Runtime.DryRun() {
		reporter.Print(os.Stdout, readings)
		return nil
	}

	cache := statecache.Disabled()
	if cfg.Runtime.OnlyChanged() {
		cache = statecache.Load(statecache.PathFor(configPath), cfg.Runtime.ResendAfter())
	}

	if filtered := reporter.Filter(readings, cache); len(filtered) < len(readings) {
		log.Debug().
			Int("total", len(readings)).
			Int("changed", len(filtered)).
			Msg("Часть значений не изменилась и не отправляется")
		readings = filtered
	}

	if err := reporter.Send(ctx, cfg.Mimismart, readings, cache); err != nil {
		return err
	}
	if err := cache.Save(); err != nil {
		// Кэш — оптимизация, а не источник истины: не смогли сохранить,
		// значит в следующий раз отправим всё заново.
		log.Warn().Err(err).Msg("Не удалось сохранить кэш отправленных значений")
	}
	return nil
}

// apply выполняет команду включения/выключения/переключения.
func apply(ctx context.Context, client *shelly.Client, cmd *command.Command) (*shelly.RelayState, error) {
	ch := shelly.Channel(cmd.Channel)
	switch cmd.Action {
	case command.ActionOn:
		return client.On(ctx, ch)
	case command.ActionOff:
		return client.Off(ctx, ch)
	case command.ActionToggle:
		return client.Toggle(ctx, ch)
	default:
		return nil, fmt.Errorf("действие %q не является командой управления", cmd.Action)
	}
}

// targetPoint задаёт формат отправки для каждой цели.
//
// Состояния и флаги уходят числом 1/0, а мощность и энергия — текстом:
// протокол передаёт числа как fixed-point 8.8, и ватты с киловатт-часами
// в этот диапазон не помещаются.
func targetPoint(name string, addr command.Addr) config.Point {
	p := config.Point{ID: addr.ID, SubID: addr.SubID}
	switch name {
	case command.TargetState, command.TargetOverpower, command.TargetOvertemp:
		// Лампа и флаги хранят состояние в одном байте, а не в датчике.
		p.As = config.AsByte
	case command.TargetPower:
		p.As, p.Unit, p.Precision = "text", " Вт", digits(1)
	case command.TargetEnergy:
		p.As, p.Unit, p.Precision = "text", " кВ", digits(2)
	case command.TargetVoltage:
		// 230 В не помещается в fixed-point 8.8, поэтому только текстом.
		p.As, p.Unit, p.Precision = "text", " В", digits(1)
	}
	return p
}

func digits(n int) *int { return &n }

// collectTargets раскладывает ответ устройства по адресам из команды.
// Каналов может быть несколько: цели вида "state0"/"state1" позволяют одной
// командой обновить оба выхода, а заодно общие для устройства значения.
func collectTargets(cmd *command.Command, status *shelly.Status) ([]reporter.Reading, error) {
	readings := make([]reporter.Reading, 0, len(cmd.Targets))

	add := func(name string, addr command.Addr, value float64, label string) {
		readings = append(readings, reporter.Reading{
			Point: targetPoint(name, addr),
			Value: value,
			Label: label,
		})
	}

	for _, ch := range cmd.TargetChannels() {
		if ch >= len(status.Relays) {
			log.Warn().
				Str("ip", cmd.IP).
				Int("channel", ch).
				Msg("Устройство не вернуло состояние канала, пропускаем")
			continue
		}
		label := fmt.Sprintf("%s ch%d", cmd.IP, ch)
		relay := status.Relays[ch]

		if addr, ok := cmd.ChannelTarget(command.TargetState, ch); ok {
			add(command.TargetState, addr, reporter.BoolValue(relay.IsOn), label)
		}
		if addr, ok := cmd.ChannelTarget(command.TargetOverpower, ch); ok {
			add(command.TargetOverpower, addr, reporter.BoolValue(relay.Overpower), label+" (перегрузка)")
		}

		powerAddr, wantPower := cmd.ChannelTarget(command.TargetPower, ch)
		energyAddr, wantEnergy := cmd.ChannelTarget(command.TargetEnergy, ch)
		if !wantPower && !wantEnergy {
			continue
		}
		if ch >= len(status.Meters) {
			log.Warn().
				Str("ip", cmd.IP).
				Int("channel", ch).
				Msg("Нет данных измерителя мощности для канала")
			continue
		}
		meter := status.Meters[ch]

		if wantPower {
			add(command.TargetPower, powerAddr, meter.Power, label+" (Вт)")
		}
		if wantEnergy {
			add(command.TargetEnergy, energyAddr, meter.Total/wattMinutesPerKWh, label+" (кВ)")
		}
	}

	// Значения, общие для всего устройства.
	if addr, ok := cmd.Target(command.TargetVoltage); ok {
		add(command.TargetVoltage, addr, status.Voltage, cmd.IP+" (В)")
	}
	if addr, ok := cmd.Target(command.TargetTemp); ok {
		add(command.TargetTemp, addr, status.Tmp.TC, cmd.IP+" (°C)")
	}
	if addr, ok := cmd.Target(command.TargetOvertemp); ok {
		add(command.TargetOvertemp, addr, reporter.BoolValue(status.OverTemperature), cmd.IP+" (перегрев)")
	}

	if len(readings) == 0 {
		return nil, fmt.Errorf("ни одна из целей команды не заполнена данными")
	}
	return readings, nil
}

// collect превращает ответ /status в набор значений по привязке из конфига.
func collect(device *config.Device, cmd *command.Command, status *shelly.Status) ([]reporter.Reading, error) {
	channels := device.Channels
	if !cmd.IsAllChannels() {
		ch, err := device.FindChannel(cmd.Channel)
		if err != nil {
			return nil, err
		}
		channels = []config.Channel{*ch}
	}

	readings := make([]reporter.Reading, 0, len(channels)+2)
	for i := range channels {
		ch := channels[i]
		if ch.Channel >= len(status.Relays) {
			log.Warn().
				Str("ip", device.IP).
				Int("channel", ch.Channel).
				Int("relays_in_status", len(status.Relays)).
				Msg("Устройство не вернуло состояние канала, пропускаем")
			continue
		}

		readings = append(readings, reporter.Reading{
			Point: ch.State.WithByteDefault(),
			Value: reporter.BoolValue(status.Relays[ch.Channel].IsOn),
			Label: channelLabel(device, &ch),
		})

		if ch.Overpower != nil {
			readings = append(readings, reporter.Reading{
				Point: ch.Overpower.WithByteDefault(),
				Value: reporter.BoolValue(status.Relays[ch.Channel].Overpower),
				Label: channelLabel(device, &ch) + " (перегрузка)",
			})
		}

		if ch.Power == nil && ch.Energy == nil {
			continue
		}
		if ch.Channel >= len(status.Meters) {
			log.Warn().
				Str("ip", device.IP).
				Int("channel", ch.Channel).
				Msg("Нет данных измерителя мощности для канала")
			continue
		}
		meter := status.Meters[ch.Channel]

		if ch.Power != nil {
			readings = append(readings, reporter.Reading{
				Point: *ch.Power,
				Value: meter.Power,
				Label: channelLabel(device, &ch) + " (Вт)",
			})
		}
		if ch.Energy != nil {
			readings = append(readings, reporter.Reading{
				Point: *ch.Energy,
				Value: meter.Total / wattMinutesPerKWh,
				Label: channelLabel(device, &ch) + " (кВ)",
			})
		}
	}

	// Температура и перегрев — общие на устройство, отдаём один раз.
	if device.Temperature != nil {
		readings = append(readings, reporter.Reading{
			Point: *device.Temperature,
			Value: status.Tmp.TC,
			Label: deviceLabel(device) + " (°C)",
		})
	}
	if device.Overtemperature != nil {
		readings = append(readings, reporter.Reading{
			Point: device.Overtemperature.WithByteDefault(),
			Value: reporter.BoolValue(status.OverTemperature),
			Label: deviceLabel(device) + " (перегрев)",
		})
	}

	if len(readings) == 0 {
		return nil, fmt.Errorf("устройство %s не вернуло данных по запрошенным каналам", device.IP)
	}
	return readings, nil
}

// sleepCtx ждёт указанное время или отмену контекста.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func commandLabel(cmd *command.Command) string {
	return fmt.Sprintf("%s ch%d", cmd.IP, cmd.Channel)
}

func deviceLabel(d *config.Device) string {
	if d.Name != "" {
		return d.Name
	}
	return d.IP
}

func channelLabel(d *config.Device, ch *config.Channel) string {
	if ch.Name != "" {
		return ch.Name
	}
	return fmt.Sprintf("%s ch%d", deviceLabel(d), ch.Channel)
}
