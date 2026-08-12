// Package reporter отправляет значения в умный дом MimiSmart через shClient.
package reporter

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"

	"github.com/staskuznec/mqtt2mimismart/internal/config"
	"github.com/staskuznec/mqtt2mimismart/internal/statecache"
	"github.com/staskuznec/mqtt2mimismart/internal/transport/api/shClient"
)

// Пауза между пакетами и время ожидания ответов сервера подобраны так же,
// как в microart2mimismart: сервер закрывает соединение, если клиент
// отправил данные и сразу отключился, не вычитав ответ.
const (
	packetDelay   = 10 * time.Millisecond
	settleDelay   = 200 * time.Millisecond
	drainDuration = 500 * time.Millisecond
)

// MaxSensorValue — максимум, представимый в протоколе SHS.
//
// PackSensor пакует значение как uint16(v*256), то есть fixed-point 8.8.
// Всё, что больше 65535/256, переполняется и приезжает в умный дом мусором;
// отрицательные значения при конверсии float→uint16 в Go дают
// implementation-dependent результат. Поэтому значения клампятся, а факт
// обрезки пишется в лог — молча портить данные хуже, чем потерять точность.
const MaxSensorValue = 255.99609375

// Reading — одно значение, уходящее в умный дом.
// Состояние реле передаётся как 1 (включено) или 0 (выключено).
type Reading struct {
	Point config.Point
	Value float64
	Label string // человекочитаемое имя для логов
}

// prepare применяет масштаб точки и приводит значение в диапазон протокола.
func prepare(r Reading) float64 {
	return clamp(r.Point.Apply(r.Value), r)
}

// Format собирает текстовое представление значения для текстовых элементов.
// Масштаб применяется, а обрезка по диапазону — нет: в текст влезает всё,
// в этом и смысл текстовых элементов.
func Format(r Reading) string {
	return fmt.Sprintf("%.*f%s", r.Point.Digits(), r.Point.Apply(r.Value), r.Point.Unit)
}

// clamp приводит значение в диапазон, представимый протоколом.
func clamp(v float64, r Reading) float64 {
	switch {
	case v < 0:
		log.Warn().
			Str("label", r.Label).
			Float64("value", v).
			Msg("Отрицательное значение не представимо в протоколе, отправляем 0")
		return 0
	case v > MaxSensorValue:
		log.Warn().
			Str("label", r.Label).
			Float64("value", v).
			Float64("max", MaxSensorValue).
			Msg("Значение больше максимума протокола, обрезано — задайте scale для этой точки")
		return MaxSensorValue
	default:
		return v
	}
}

// Key — адрес элемента в виде "id:subid". Используется ключом кэша.
func Key(r Reading) string {
	return fmt.Sprintf("%d:%d", r.Point.ID, r.Point.SubID)
}

// WireValue возвращает значение ровно в том виде, в каком оно уйдёт на провод.
// По нему же сравнивается «изменилось или нет».
func WireValue(r Reading) string {
	switch {
	case r.Point.AsText():
		return Format(r)
	case r.Point.AsSingleByte():
		return strconv.Itoa(int(SingleByte(r)))
	default:
		return strconv.FormatFloat(prepare(r), 'f', -1, 64)
	}
}

// SingleByte приводит значение к одному байту 0..255. Для состояний и флагов
// это 0 или 1, но диапазон оставлен полным: те же однобайтовые элементы
// используются, например, для положения штор.
func SingleByte(r Reading) uint8 {
	v := r.Point.Apply(r.Value)
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	default:
		return uint8(v + 0.5) // округляем к ближайшему
	}
}

// Filter отбрасывает значения, которые уже были отправлены в этот же элемент
// и с тех пор не изменились. Отброшенные пишутся в лог на уровне debug.
func Filter(readings []Reading, cache *statecache.Cache) []Reading {
	if cache == nil {
		return readings
	}

	kept := make([]Reading, 0, len(readings))
	for _, r := range readings {
		key, value := Key(r), WireValue(r)
		if !cache.Changed(key, value) {
			log.Debug().
				Str("addr", key).
				Str("value", value).
				Str("label", r.Label).
				Msg("Значение не изменилось, не отправляем")
			continue
		}
		kept = append(kept, r)
	}
	return kept
}

// Send открывает соединение с сервером умного дома, отправляет все значения
// одним батчем и корректно закрывает соединение.
//
// Если передан кэш, отправленные значения в нём запоминаются — чтобы в
// следующий раз не слать то же самое ещё раз. Фильтрация выполняется до
// вызова, функцией Filter.
func Send(ctx context.Context, cfg config.Mimismart, readings []Reading, cache *statecache.Cache) error {
	if len(readings) == 0 {
		log.Debug().Msg("Nothing to report")
		return nil
	}

	shc := shClient.NewShClient(ctx, cfg.Addr, cfg.Key, true, cfg.Mac)
	log.Debug().Str("addr", cfg.Addr).Msg("Connecting to SHS")
	if err := shc.Start(); err != nil {
		return errors.Wrap(err, "start shclient")
	}
	defer func() {
		if err := shc.Close(); err != nil {
			log.Debug().Err(err).Msg("close shclient")
		}
	}()

	// Сначала пакуем всё, потом отправляем — так же, как в microart2mimismart.
	packets := make([][]byte, 0, len(readings))
	for _, r := range readings {
		if r.Point.AsText() {
			text := Format(r)
			log.Debug().
				Uint16("id", r.Point.ID).
				Uint8("subid", r.Point.SubID).
				Str("text", text).
				Str("label", r.Label).
				Msg("Built text packet")
			packets = append(packets, shc.PackStatusText(r.Point.ID, r.Point.SubID, text))
			continue
		}

		if r.Point.AsSingleByte() {
			b := SingleByte(r)
			log.Debug().
				Uint16("id", r.Point.ID).
				Uint8("subid", r.Point.SubID).
				Uint8("byte", b).
				Str("label", r.Label).
				Msg("Built single-byte packet")
			packets = append(packets, shc.PackRaw(r.Point.ID, r.Point.SubID, []byte{b}))
			continue
		}

		value := prepare(r)
		log.Debug().
			Uint16("id", r.Point.ID).
			Uint8("subid", r.Point.SubID).
			Float64("raw", r.Value).
			Float64("value", value).
			Str("label", r.Label).
			Msg("Built sensor packet")
		packets = append(packets, shc.PackSensor(r.Point.ID, r.Point.SubID, value))
	}

	log.Debug().Int("count", len(packets)).Msg("Sending packets to MimiSmart")
	for i, pkt := range packets {
		if err := shc.WriteFull(pkt); err != nil {
			log.Error().Err(err).Int("index", i).Msg("Failed to send packet")
			continue
		}
		// Запоминаем только то, что действительно ушло: если запись упала,
		// в следующий раз значение отправится заново.
		if cache != nil {
			cache.Remember(Key(readings[i]), WireValue(readings[i]))
		}
		time.Sleep(packetDelay)
	}

	// Даём серверу ответить и вычитываем его ответы, иначе он может
	// закрыть соединение, решив, что клиент не слушает.
	time.Sleep(settleDelay)
	shc.DrainResponses(drainDuration)

	log.Debug().Int("count", len(packets)).Msg("All packets sent")
	return nil
}

// BoolValue переводит состояние реле в значение для умного дома.
func BoolValue(on bool) float64 {
	if on {
		return 1
	}
	return 0
}

// Print печатает то, что ушло бы в умный дом, не открывая соединение.
// Используется в сухом прогоне: видно и исходное значение, и то, во что оно
// превращается после масштабирования и обрезки под протокол.
func Print(w io.Writer, readings []Reading) {
	if len(readings) == 0 {
		fmt.Fprintln(w, "сухой прогон: отправлять нечего")
		return
	}
	fmt.Fprintf(w, "сухой прогон, в умный дом ушло бы %d значений:\n", len(readings))
	for _, r := range readings {
		if r.Point.AsText() {
			fmt.Fprintf(w, "  id=%d subid=%d  %-28s %q (текст)\n",
				r.Point.ID, r.Point.SubID, r.Label, Format(r))
			continue
		}
		if r.Point.AsSingleByte() {
			fmt.Fprintf(w, "  id=%d subid=%d  %-28s %d (один байт)\n",
				r.Point.ID, r.Point.SubID, r.Label, SingleByte(r))
			continue
		}
		value := prepare(r)
		if value == r.Value {
			fmt.Fprintf(w, "  id=%d subid=%d  %-28s %g\n", r.Point.ID, r.Point.SubID, r.Label, value)
			continue
		}
		fmt.Fprintf(w, "  id=%d subid=%d  %-28s %g (исходное %g)\n",
			r.Point.ID, r.Point.SubID, r.Label, value, r.Value)
	}
}
