// Package link — связки между топиками MQTT и элементами умного дома.
//
// Связка описывает одно направление: либо сообщение с шины кладётся в элемент,
// либо изменение элемента публикуется в топик. Двусторонняя лампа — это две
// связки, и так честнее: у направлений разные поля, разные формы значения и
// свои счётчики ошибок.
package link

import (
	"fmt"
	"strings"
)

// Direction — куда течёт значение.
type Direction string

const (
	// In — с шины в умный дом: состояние устройства становится значением элемента.
	In Direction = "in"
	// Out — из умного дома на шину: нажатие становится командой устройству.
	Out Direction = "out"
)

// Способ достать значение из полезной нагрузки (для направления In).
const (
	ExtractRaw  = "raw"  // вся нагрузка целиком
	ExtractJSON = "json" // путь в JSON: "relays.0.ison"
)

// Форма значения на проводе протокола SHS.
//
// Это свойство элемента умного дома, а не устройства, и ошибка здесь стоит
// дороже всего: у однобайтовых элементов значение датчика для единицы даёт
// на проводе 00 01, младший байт нулевой, и лампа остаётся выключенной.
// Выключение при этом «работает», потому что ноль даёт нули в обоих байтах.
const (
	EncodeByte   = "byte"   // один сырой байт: лампа, скрипт, клапан, шторы
	EncodeSensor = "sensor" // fixed-point 8.8, два байта: датчики
	EncodeText   = "text"   // строка: всё, что не влезает в 8.8
)

// Способ прочитать значение элемента (для направления Out).
const (
	DecodeByte   = "byte"
	DecodeSensor = "sensor"
	DecodeText   = "text"

	// DecodeLamp — семантика однобайтовых элементов с нажатием.
	//
	//	0xFF   нажатие в интерфейсе, то есть запрос на переключение
	//	1 и 8  включить
	//	0 и 9  выключить
	//
	// Это знание раньше жило в скрипте умного дома и дублировалось в каждом.
	DecodeLamp = "lamp"
)

// Kind — назначение исходящей связки.
//
// Различие не косметическое. Сохранённая команда означает реле, которое
// щёлкает само при включении: брокер отдаёт её устройству, как только то
// подключилось. А сохранённое показание, наоборот, нужно — без него новый
// подписчик висит с пустотой до следующего изменения, а датчик может не
// меняться часами.
const (
	KindCommand = "command" // команда устройству; retain запрещён
	KindState   = "state"   // показание наружу; retain уместен
)

// Значения, которые DecodeLamp выдаёт наружу.
const (
	StateOn     = "on"
	StateOff    = "off"
	StateToggle = "toggle"
)

// Link — одна связка.
type Link struct {
	ID       int64
	DeviceID int64 // 0 — связка сама по себе, вне устройства

	// Device — название устройства, которому связка принадлежит.
	//
	// В базе не хранится: имя живёт у устройства, и держать его копию в
	// каждой связке значило бы однажды переименовать устройство и остаться со
	// старым именем в журнале. Подставляется при чтении из базы.
	Device string

	// PairID связывает две стороны двусторонней привязки. Ноль означает
	// одностороннюю связку. Обе стороны носят один и тот же идентификатор —
	// идентификатор первой из них.
	PairID  int64
	Name    string
	Enabled bool

	Direction Direction

	// Topic для направления In — фильтр подписки, допускающий "+" и "#".
	// Для Out — конкретный топик публикации, без подстановочных знаков.
	Topic  string
	QoS    byte
	Retain bool

	// Kind — назначение исходящей связки: команда устройству или показание
	// наружу. Пусто означает команду: это и чаще, и строже по проверкам.
	Kind string

	// Извлечение значения из нагрузки (только In).
	Extract     string
	ExtractPath string

	// Values — таблица перевода значений. Для In переводит текст нагрузки в
	// число ("on" → 1), для Out — значение элемента в текст команды
	// ("toggle" → "toggle"). Пустая таблица означает «оставить как есть».
	Values map[string]string

	// Числовые поправки, применяются после таблицы (только In).
	Scale  float64 // ноль означает 1: множитель без действия
	Offset float64

	// Элемент умного дома.
	TargetID    uint16
	TargetSubID uint8

	// Форма значения: Encode для In, Decode для Out.
	Encode string
	Decode string
	Unit   string // приписка к текстовому значению, например " Вт"

	// Precision — знаков после запятой в текстовом значении.
	// nil означает «как есть, без округления»: ноль здесь занят и означает
	// округление до целого, иначе 42.3 молча превращалось бы в 42.
	Precision *int

	// OnlyChanged отбрасывает значения, совпадающие с прошлым отправленным.
	OnlyChanged bool
}

// Addr возвращает адрес элемента в виде "id:subid".
func (l Link) Addr() string { return fmt.Sprintf("%d:%d", l.TargetID, l.TargetSubID) }

// Title — как связку называть в журнале и в интерфейсе.
func (l Link) Title() string {
	name := l.Name
	if name == "" {
		if l.Direction == Out {
			name = l.Addr() + " → " + l.Topic
		} else {
			name = l.Topic + " → " + l.Addr()
		}
	}

	// Имя связки приходит из профиля и у одинаковых устройств совпадает: на
	// объекте с тремя инверторами «Ток разряда АКБ» в журнале не говорит
	// ничего. Поэтому впереди идёт устройство.
	if l.Device != "" {
		return l.Device + " · " + name
	}
	return name
}

// Normalize приводит необязательные поля к явному виду.
//
// Нужна перед записью в базу: значения по умолчанию из схемы не срабатывают,
// потому что столбцы всегда пишутся явно. А пустая строка вместо назначения
// потом выглядела бы в вебе пустым полем выбора, хотя смысл у неё есть.
func (l Link) Normalize() Link {
	if l.Direction == In && l.Extract == "" {
		l.Extract = ExtractRaw
	}
	// Назначение есть только у исходящих: входящая связка ничего никуда не
	// публикует, и различать команду с показанием ей незачем.
	if l.Direction == Out && l.Kind == "" {
		l.Kind = KindCommand
	}
	return l
}

// Validate проверяет связку целиком. Вызывается при сохранении формы: пустить
// в работу заведомо нерабочую связку хуже, чем отказать сразу.
func (l Link) Validate() error {
	switch l.Direction {
	case In:
		return l.validateIn()
	case Out:
		return l.validateOut()
	default:
		return fmt.Errorf("направление %q: допустимы %q и %q", l.Direction, In, Out)
	}
}

func (l Link) validateIn() error {
	if !ValidFilter(l.Topic) {
		return fmt.Errorf("топик %q: недопустимый фильтр подписки", l.Topic)
	}
	switch l.Extract {
	case "", ExtractRaw:
	case ExtractJSON:
		if strings.TrimSpace(l.ExtractPath) == "" {
			return fmt.Errorf("извлечение из JSON: не задан путь к полю")
		}
	default:
		return fmt.Errorf("извлечение %q: допустимы %q и %q", l.Extract, ExtractRaw, ExtractJSON)
	}
	switch l.Encode {
	case EncodeByte, EncodeSensor, EncodeText:
	case "":
		return fmt.Errorf("не задана форма значения: %q, %q или %q", EncodeByte, EncodeSensor, EncodeText)
	default:
		return fmt.Errorf("форма значения %q: допустимы %q, %q и %q",
			l.Encode, EncodeByte, EncodeSensor, EncodeText)
	}
	if l.Precision != nil && *l.Precision < 0 {
		return fmt.Errorf("знаков после запятой: %d, отрицательное число недопустимо", *l.Precision)
	}
	return nil
}

func (l Link) validateOut() error {
	if !ValidTopic(l.Topic) {
		return fmt.Errorf("топик %q: для публикации нужен конкретный топик без подстановочных знаков", l.Topic)
	}
	switch l.Kind {
	case "", KindCommand:
		// Сохранённая команда — это реле, которое щёлкает само при включении:
		// брокер отдаёт её устройству, как только то подключается.
		if l.Retain {
			return fmt.Errorf("топик %q: retain на командном топике недопустим — "+
				"брокер отдаст команду устройству при следующем подключении, и оно сработает само", l.Topic)
		}
	case KindState:
		// Показание наружу: сохранять уместно, запрета нет.
	default:
		return fmt.Errorf("назначение %q: допустимы %q и %q", l.Kind, KindCommand, KindState)
	}
	if l.QoS > 2 {
		return fmt.Errorf("QoS %d: допустимы 0, 1 и 2", l.QoS)
	}
	switch l.Decode {
	case DecodeByte, DecodeSensor, DecodeText, DecodeLamp:
	case "":
		return fmt.Errorf("не задан способ чтения элемента: %q, %q, %q или %q",
			DecodeByte, DecodeSensor, DecodeText, DecodeLamp)
	default:
		return fmt.Errorf("чтение элемента %q: допустимы %q, %q, %q и %q",
			l.Decode, DecodeByte, DecodeSensor, DecodeText, DecodeLamp)
	}
	return nil
}
