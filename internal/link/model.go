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
	Name     string
	Enabled  bool

	Direction Direction

	// Topic для направления In — фильтр подписки, допускающий "+" и "#".
	// Для Out — конкретный топик публикации, без подстановочных знаков.
	Topic  string
	QoS    byte
	Retain bool

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
	if l.Name != "" {
		return l.Name
	}
	if l.Direction == Out {
		return l.Addr() + " → " + l.Topic
	}
	return l.Topic + " → " + l.Addr()
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
	// Сохранённая команда — это реле, которое щёлкает само при включении:
	// брокер отдаёт её устройству, как только то подключается.
	if l.Retain {
		return fmt.Errorf("топик %q: retain на командном топике недопустим — "+
			"брокер отдаст команду устройству при следующем подключении, и оно сработает само", l.Topic)
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
