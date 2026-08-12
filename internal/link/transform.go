package link

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Wire — то, что реально уйдёт на провод.
//
// Отдельный тип нужен панели «Проба» в вебе: она показывает не только итоговое
// значение, но и байты, которыми оно поедет. Именно на форме значения проще
// всего ошибиться, и увидеть её глазами до сохранения дороже любого описания.
type Wire struct {
	Kind    string  // byte, sensor или text
	Bytes   []byte  // что уйдёт в полезной нагрузке пакета
	Text    string  // человекочитаемое представление
	Number  float64 // значение после таблицы и поправок
	Clamped bool    // значение не помещалось в диапазон и было обрезано
}

// Hex возвращает байты в виде "00 01" — для показа в вебе.
func (w Wire) Hex() string {
	parts := make([]string, len(w.Bytes))
	for i, b := range w.Bytes {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, " ")
}

// Границы значения датчика. Протокол передаёт числа как fixed-point 8.8:
// uint16(v*256), то есть представимы только 0 … 65535/256.
const (
	sensorMin = 0.0
	sensorMax = 255.99609375
)

// ToWire прогоняет полезную нагрузку с шины через всю цепочку связки:
// извлечение, таблицу значений, поправки и форму записи.
func (l Link) ToWire(payload []byte) (Wire, error) {
	raw, err := l.extract(payload)
	if err != nil {
		return Wire{}, err
	}

	// Таблица переводит текст в число: "on" → 1, "off" → 0, "overpower" → 1.
	value := raw
	if mapped, ok := l.Values[strings.TrimSpace(raw)]; ok {
		value = mapped
	}

	switch l.Encode {
	case EncodeText:
		return l.encodeText(value)
	case EncodeByte:
		return l.encodeByte(value)
	case EncodeSensor:
		return l.encodeSensor(value)
	default:
		return Wire{}, fmt.Errorf("форма значения %q не поддерживается", l.Encode)
	}
}

// extract достаёт значение из нагрузки.
func (l Link) extract(payload []byte) (string, error) {
	switch l.Extract {
	case "", ExtractRaw:
		return string(payload), nil
	case ExtractJSON:
		return jsonPath(payload, l.ExtractPath)
	default:
		return "", fmt.Errorf("извлечение %q не поддерживается", l.Extract)
	}
}

// number приводит значение к числу и применяет поправки.
func (l Link) number(value string) (float64, error) {
	s := strings.TrimSpace(value)

	// Пустота — это не «неизвестное значение», а отсутствие данных, и совет
	// про таблицу значений тут только сбивает с толку.
	if s == "" {
		return 0, fmt.Errorf("сообщение пустое: нечего преобразовывать")
	}

	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// Логические значения приходят и словами: устройства пишут "true",
		// умный дом ждёт единицу.
		switch strings.ToLower(s) {
		case "true", "on", "yes":
			v = 1
		case "false", "off", "no":
			v = 0
		default:
			return 0, fmt.Errorf(
				"устройство прислало %q, а элементу нужно число. "+
					"Добавьте в поле «Таблица значений на вход» строку: %s = 1", value, s)
		}
	}

	if l.Scale != 0 {
		v *= l.Scale
	}
	return v + l.Offset, nil
}

// encodeByte готовит однобайтовое значение.
func (l Link) encodeByte(value string) (Wire, error) {
	v, err := l.number(value)
	if err != nil {
		return Wire{}, err
	}

	// Диапазон полный, а не только 0 и 1: те же однобайтовые элементы
	// используются, например, для положения штор.
	b, clamped := clampByte(v)
	return Wire{
		Kind:    EncodeByte,
		Bytes:   []byte{b},
		Text:    strconv.Itoa(int(b)),
		Number:  v,
		Clamped: clamped,
	}, nil
}

// encodeSensor готовит значение датчика в формате fixed-point 8.8.
func (l Link) encodeSensor(value string) (Wire, error) {
	v, err := l.number(value)
	if err != nil {
		return Wire{}, err
	}

	clamped := false
	switch {
	case v < sensorMin:
		v, clamped = sensorMin, true
	case v > sensorMax:
		v, clamped = sensorMax, true
	}

	payload := make([]byte, 2)
	binary.LittleEndian.PutUint16(payload, uint16(v*256))
	return Wire{
		Kind:    EncodeSensor,
		Bytes:   payload,
		Text:    strconv.FormatFloat(v, 'f', -1, 64),
		Number:  v,
		Clamped: clamped,
	}, nil
}

// encodeText готовит текстовое значение.
//
// Текстом уходит всё, что не помещается в fixed-point 8.8: ватты,
// киловатт-часы, 230 вольт. Ограничения диапазона тут нет, в этом и смысл.
func (l Link) encodeText(value string) (Wire, error) {
	v, err := l.number(value)
	if err != nil {
		// Не всякий текст обязан быть числом: строка может быть и просто
		// строкой — названием режима, например.
		text := strings.TrimSpace(value) + l.Unit
		return Wire{Kind: EncodeText, Bytes: []byte(text), Text: text}, nil
	}

	text := strconv.FormatFloat(v, 'f', l.digits(), 64) + l.Unit
	return Wire{Kind: EncodeText, Bytes: []byte(text), Text: text, Number: v}, nil
}

// digits возвращает число знаков после запятой для текстового значения.
// Минус единица — представление без округления, самое короткое из точных.
func (l Link) digits() int {
	if l.Precision == nil {
		return -1
	}
	return *l.Precision
}

// Value читает значение элемента умного дома до перевода по таблице.
//
// Отдельно от [Link.ToPayload] потому, что движку нужно именно исходное
// значение: по нему видно, состояние это или мгновенное действие, а перевод
// может превратить "toggle" во что угодно.
func (l Link) Value(payload []byte) (string, error) { return l.decodeValue(payload) }

// MapValue переводит значение по таблице связки.
func (l Link) MapValue(value string) string {
	if mapped, ok := l.Values[value]; ok {
		return mapped
	}
	return value
}

// Momentary сообщает, что значение описывает мгновенное действие, а не
// состояние.
//
// Различие существенное. Состояние приходит снова и снова — в каждом ответе на
// запрос состояний, — и повторы надо отсеивать. Нажатие же повторяется по делу:
// нажали дважды, значит переключить надо дважды, и отсев здесь проглотил бы
// второе нажатие.
func Momentary(value string) bool { return value == StateToggle }

// ToPayload читает значение элемента умного дома и готовит полезную нагрузку для
// публикации в топик.
func (l Link) ToPayload(payload []byte) (string, error) {
	value, err := l.Value(payload)
	if err != nil {
		return "", err
	}
	return l.MapValue(value), nil
}

func (l Link) decodeValue(payload []byte) (string, error) {
	switch l.Decode {
	case DecodeText:
		return string(payload), nil

	case DecodeByte:
		if len(payload) < 1 {
			return "", fmt.Errorf("пустая нагрузка, ожидался один байт")
		}
		return strconv.Itoa(int(payload[0])), nil

	case DecodeSensor:
		if len(payload) < 2 {
			return "", fmt.Errorf("нагрузка в %d байт, для датчика нужно два", len(payload))
		}
		raw := binary.LittleEndian.Uint16(payload[:2])
		return strconv.FormatFloat(float64(raw)/256, 'f', -1, 64), nil

	case DecodeLamp:
		return decodeLamp(payload)

	default:
		return "", fmt.Errorf("чтение элемента %q не поддерживается", l.Decode)
	}
}

// decodeLamp разбирает состояние однобайтового элемента с нажатием.
//
// Значения взяты из скриптов умного дома, где эта логика жила раньше:
// 0xFF — нажатие в интерфейсе, 1 и 8 — включение, 0 и 9 — выключение.
// Разные значения для одного действия появляются оттого, что команда может
// прийти и от логики, и от привязанного выключателя.
func decodeLamp(payload []byte) (string, error) {
	if len(payload) < 1 {
		return "", fmt.Errorf("пустая нагрузка, ожидался один байт")
	}
	switch payload[0] {
	case 0xFF:
		return StateToggle, nil
	case 1, 8:
		return StateOn, nil
	case 0, 9:
		return StateOff, nil
	default:
		return "", fmt.Errorf("значение %d не является состоянием лампы (ожидались 0, 1, 8, 9 или 0xFF)", payload[0])
	}
}

// clampByte приводит число к одному байту.
func clampByte(v float64) (uint8, bool) {
	switch {
	case v <= 0:
		return 0, v < 0
	case v >= 255:
		return 255, v > 255
	default:
		return uint8(v + 0.5), false // округляем к ближайшему
	}
}

// jsonPath достаёт поле из JSON по пути вида "relays.0.ison".
//
// Свой разбор вместо библиотеки: путей нужно ровно два вида — по имени поля и
// по номеру в массиве, — а тянуть ради этого зависимость незачем.
func jsonPath(payload []byte, path string) (string, error) {
	var doc any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return "", fmt.Errorf("нагрузка не разбирается как JSON: %w", err)
	}

	current := doc
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[part]
			if !ok {
				return "", fmt.Errorf("в JSON нет поля %q (путь %q)", part, path)
			}
			current = value
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil {
				return "", fmt.Errorf("путь %q: %q не номер элемента массива", path, part)
			}
			if i < 0 || i >= len(node) {
				return "", fmt.Errorf("путь %q: в массиве %d элементов, запрошен %d", path, len(node), i)
			}
			current = node[i]
		default:
			return "", fmt.Errorf("путь %q: на шаге %q значение не объект и не массив", path, part)
		}
	}

	return stringify(current)
}

// stringify приводит значение из JSON к строке.
func stringify(v any) (string, error) {
	switch value := v.(type) {
	case string:
		return value, nil
	case bool:
		return strconv.FormatBool(value), nil
	case float64:
		// JSON не различает целые и дробные, а "41" читается глазами лучше,
		// чем "41.000000".
		return strconv.FormatFloat(value, 'f', -1, 64), nil
	case nil:
		return "", fmt.Errorf("значение по указанному пути равно null")
	default:
		return "", fmt.Errorf("значение по указанному пути — составное, укажите путь до поля")
	}
}
