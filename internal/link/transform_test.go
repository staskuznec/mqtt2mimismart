package link

import (
	"testing"
)

// digits — сокращение для необязательной точности в тестах.
func digits(n int) *int { return &n }

// Состояние реле Shelly приходит словом, а лампа умного дома хранит его в
// одном байте. Это самая частая связка во всём шлюзе.
func TestToWireRelayState(t *testing.T) {
	l := Link{
		Direction: In,
		Topic:     "shellies/shelly25-A1/relay/0",
		Encode:    EncodeByte,
		Values:    map[string]string{"on": "1", "off": "0", "overpower": "1"},
	}

	for _, tc := range []struct {
		payload string
		want    byte
	}{
		{"on", 1},
		{"off", 0},
		{"overpower", 1},
	} {
		t.Run(tc.payload, func(t *testing.T) {
			w, err := l.ToWire([]byte(tc.payload))
			if err != nil {
				t.Fatalf("ToWire: %v", err)
			}
			if len(w.Bytes) != 1 {
				t.Fatalf("на провод уходит %d байт, лампа принимает ровно один", len(w.Bytes))
			}
			if w.Bytes[0] != tc.want {
				t.Errorf("байт = %d, ожидался %d", w.Bytes[0], tc.want)
			}
		})
	}
}

// Тот же случай, но формой значения ошиблись: выбран датчик вместо байта.
// Единица уезжает как 00 01, младший байт нулевой, и лампа остаётся
// выключенной, — а выключение при этом «работает». Тест фиксирует, что
// разница именно в этом, чтобы диагностика в вебе была честной.
func TestSensorFormBreaksLamps(t *testing.T) {
	asByte := Link{Encode: EncodeByte, Values: map[string]string{"on": "1"}}
	asSensor := Link{Encode: EncodeSensor, Values: map[string]string{"on": "1"}}

	wb, err := asByte.ToWire([]byte("on"))
	if err != nil {
		t.Fatalf("байт: %v", err)
	}
	ws, err := asSensor.ToWire([]byte("on"))
	if err != nil {
		t.Fatalf("датчик: %v", err)
	}

	if wb.Hex() != "01" {
		t.Errorf("однобайтовая форма дала %q, ожидалось \"01\"", wb.Hex())
	}
	// Младший байт первый: 1.0 в формате 8.8 это 0x0100 little-endian.
	if ws.Hex() != "00 01" {
		t.Errorf("форма датчика дала %q, ожидалось \"00 01\"", ws.Hex())
	}
	if ws.Bytes[0] != 0 {
		t.Error("младший байт не нулевой — тогда описание ловушки в документации неверно")
	}
}

// Ватты не помещаются в fixed-point 8.8, поэтому уходят текстом с единицей.
func TestToWirePowerAsText(t *testing.T) {
	l := Link{Encode: EncodeText, Unit: " Вт", Precision: digits(1)}

	w, err := l.ToWire([]byte("41.23"))
	if err != nil {
		t.Fatalf("ToWire: %v", err)
	}
	if w.Text != "41.2 Вт" {
		t.Errorf("текст = %q, ожидалось %q", w.Text, "41.2 Вт")
	}
}

// Энергия у Shelly считается в ватт-минутах, а показывать надо киловатт-часы.
// Множитель — это то самое место, где ошибка тихая и ровно в шестьдесят раз.
func TestToWireEnergyScale(t *testing.T) {
	l := Link{Encode: EncodeText, Scale: 1.0 / 60000, Unit: " кВт·ч", Precision: digits(2)}

	w, err := l.ToWire([]byte("211200")) // 211200 Вт·мин = 3.52 кВт·ч
	if err != nil {
		t.Fatalf("ToWire: %v", err)
	}
	if w.Text != "3.52 кВт·ч" {
		t.Errorf("текст = %q, ожидалось %q", w.Text, "3.52 кВт·ч")
	}
}

// Температура — единственная величина, которая помещается в датчик как есть.
func TestToWireTemperatureAsSensor(t *testing.T) {
	l := Link{Encode: EncodeSensor}

	w, err := l.ToWire([]byte("42.5"))
	if err != nil {
		t.Fatalf("ToWire: %v", err)
	}
	// 42.5 * 256 = 10880 = 0x2A80, little-endian: 80 2a
	if w.Hex() != "80 2a" {
		t.Errorf("байты = %q, ожидалось %q", w.Hex(), "80 2a")
	}
}

// 230 вольт в датчик не влезают. Обрезка обязана быть заметной, иначе в
// умном доме появится правдоподобное, но неверное показание.
func TestToWireSensorClamps(t *testing.T) {
	l := Link{Encode: EncodeSensor}

	w, err := l.ToWire([]byte("231.4"))
	if err != nil {
		t.Fatalf("ToWire: %v", err)
	}
	if w.Clamped {
		t.Error("231.4 помещается в диапазон, обрезка не нужна")
	}

	w, err = l.ToWire([]byte("400"))
	if err != nil {
		t.Fatalf("ToWire: %v", err)
	}
	if !w.Clamped {
		t.Error("400 не помещается в fixed-point 8.8, а обрезка не отмечена")
	}
}

func TestToWireBooleanWords(t *testing.T) {
	l := Link{Encode: EncodeByte}

	for _, tc := range []struct {
		payload string
		want    byte
	}{
		{"true", 1},
		{"false", 0},
		{"1", 1},
		{"0", 0},
	} {
		t.Run(tc.payload, func(t *testing.T) {
			w, err := l.ToWire([]byte(tc.payload))
			if err != nil {
				t.Fatalf("ToWire: %v", err)
			}
			if w.Bytes[0] != tc.want {
				t.Errorf("байт = %d, ожидался %d", w.Bytes[0], tc.want)
			}
		})
	}
}

// Неизвестное слово должно давать понятную ошибку, а не тихий ноль: ноль в
// лампе выглядит как «выключено» и ищется потом долго.
func TestToWireUnknownValueFails(t *testing.T) {
	l := Link{Encode: EncodeByte}

	if _, err := l.ToWire([]byte("странное")); err == nil {
		t.Error("неизвестное значение принято без ошибки")
	}
}

func TestToWireJSONPath(t *testing.T) {
	payload := []byte(`{"relays":[{"ison":true,"overpower":false},{"ison":false}],"temperature":42.3}`)

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"поле объекта", "temperature", "42.3"},
		{"элемент массива", "relays.0.ison", "1"},
		{"второй канал", "relays.1.ison", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := Link{Extract: ExtractJSON, ExtractPath: tc.path, Encode: EncodeText}
			w, err := l.ToWire(payload)
			if err != nil {
				t.Fatalf("ToWire: %v", err)
			}
			if w.Text != tc.want {
				t.Errorf("значение = %q, ожидалось %q", w.Text, tc.want)
			}
		})
	}
}

func TestToWireJSONErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		path    string
	}{
		{"не json", "on", "ison"},
		{"нет поля", `{"a":1}`, "b"},
		{"выход за массив", `{"relays":[{"ison":true}]}`, "relays.5.ison"},
		{"составное значение", `{"relays":[{"ison":true}]}`, "relays"},
		{"null", `{"voltage":null}`, "voltage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := Link{Extract: ExtractJSON, ExtractPath: tc.path, Encode: EncodeText}
			if _, err := l.ToWire([]byte(tc.payload)); err == nil {
				t.Error("ошибки нет, хотя путь неразрешим")
			}
		})
	}
}

// Нажатие лампы в интерфейсе приезжает как 0xFF и означает «переключи».
// Shelly понимает toggle сам, поэтому хранить состояние на нашей стороне
// не нужно — этим и отличается новая схема от скриптов.
func TestToPayloadLamp(t *testing.T) {
	l := Link{
		Direction: Out,
		Decode:    DecodeLamp,
		Values:    map[string]string{StateOn: "on", StateOff: "off", StateToggle: "toggle"},
	}

	for _, tc := range []struct {
		name    string
		payload []byte
		want    string
	}{
		{"нажатие в интерфейсе", []byte{0xFF}, "toggle"},
		{"включение от логики", []byte{1}, "on"},
		{"включение выключателем", []byte{8}, "on"},
		{"выключение от логики", []byte{0}, "off"},
		{"выключение выключателем", []byte{9}, "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := l.ToPayload(tc.payload)
			if err != nil {
				t.Fatalf("ToPayload: %v", err)
			}
			if got != tc.want {
				t.Errorf("нагрузка = %q, ожидалась %q", got, tc.want)
			}
		})
	}
}

func TestToPayloadLampUnknown(t *testing.T) {
	l := Link{Decode: DecodeLamp}
	if _, err := l.ToPayload([]byte{42}); err == nil {
		t.Error("значение 42 принято как состояние лампы")
	}
	if _, err := l.ToPayload(nil); err == nil {
		t.Error("пустая нагрузка принята")
	}
}

func TestToPayloadSensor(t *testing.T) {
	l := Link{Decode: DecodeSensor}

	got, err := l.ToPayload([]byte{0x80, 0x2a}) // 0x2A80 / 256 = 42.5
	if err != nil {
		t.Fatalf("ToPayload: %v", err)
	}
	if got != "42.5" {
		t.Errorf("значение = %q, ожидалось %q", got, "42.5")
	}

	if _, err := l.ToPayload([]byte{0x80}); err == nil {
		t.Error("однобайтовая нагрузка принята как значение датчика")
	}
}

func TestValidateIn(t *testing.T) {
	base := Link{Direction: In, Topic: "shellies/+/relay/0", Encode: EncodeByte}
	if err := base.Validate(); err != nil {
		t.Fatalf("корректная связка отвергнута: %v", err)
	}

	for _, tc := range []struct {
		name string
		mut  func(*Link)
	}{
		{"плохой фильтр", func(l *Link) { l.Topic = "shellies/#/relay" }},
		{"нет формы значения", func(l *Link) { l.Encode = "" }},
		{"неизвестная форма", func(l *Link) { l.Encode = "нечто" }},
		{"json без пути", func(l *Link) { l.Extract = ExtractJSON; l.ExtractPath = "" }},
		{"отрицательная точность", func(l *Link) { l.Precision = digits(-1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := base
			tc.mut(&l)
			if err := l.Validate(); err == nil {
				t.Error("связка принята, хотя работать не будет")
			}
		})
	}
}

// retain на командном топике означает реле, которое щёлкает само при
// включении: брокер отдаёт ему сохранённую команду.
func TestValidateOutRejectsRetain(t *testing.T) {
	l := Link{
		Direction: Out,
		Topic:     "shellies/shelly25-A1/relay/0/command",
		Decode:    DecodeLamp,
		Retain:    true,
	}
	if err := l.Validate(); err == nil {
		t.Error("retain на командном топике принят")
	}
}

func TestValidateOut(t *testing.T) {
	base := Link{Direction: Out, Topic: "shellies/shelly25-A1/relay/0/command", Decode: DecodeLamp}
	if err := base.Validate(); err != nil {
		t.Fatalf("корректная связка отвергнута: %v", err)
	}

	for _, tc := range []struct {
		name string
		mut  func(*Link)
	}{
		{"подстановка в топике", func(l *Link) { l.Topic = "shellies/+/relay/0/command" }},
		{"нет способа чтения", func(l *Link) { l.Decode = "" }},
		{"неизвестный способ", func(l *Link) { l.Decode = "нечто" }},
		{"недопустимый QoS", func(l *Link) { l.QoS = 3 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := base
			tc.mut(&l)
			if err := l.Validate(); err == nil {
				t.Error("связка принята, хотя работать не будет")
			}
		})
	}
}

func TestTitle(t *testing.T) {
	in := Link{Direction: In, Topic: "shellies/a/relay/0", TargetID: 563, TargetSubID: 57}
	if got := in.Title(); got != "shellies/a/relay/0 → 563:57" {
		t.Errorf("Title() = %q", got)
	}

	out := Link{Direction: Out, Topic: "shellies/a/relay/0/command", TargetID: 563, TargetSubID: 57}
	if got := out.Title(); got != "563:57 → shellies/a/relay/0/command" {
		t.Errorf("Title() = %q", got)
	}

	named := Link{Name: "Прихожая, канал 0", Direction: In}
	if got := named.Title(); got != "Прихожая, канал 0" {
		t.Errorf("Title() = %q, ожидалось имя связки", got)
	}
}
