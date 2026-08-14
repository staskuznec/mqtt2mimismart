package devtmpl

import (
	"strings"
	"testing"

	"github.com/staskuznec/mqtt2mimismart/internal/link"
)

// Профили пишутся по документации производителя, а проверить их на железе
// получается далеко не сразу. Ошибка при этом выходит тихая: неверный путь в
// JSON не ломает ничего при сохранении, связка просто молчит, и разбираться с
// этим приходится уже на объекте.
//
// Поэтому здесь профиль прогоняется на настоящей нагрузке, снятой из
// документации, и проверяется то самое значение, которое уедет в умный дом.
func TestProfilesOnRealPayloads(t *testing.T) {
	// Нагрузка Shelly Gen2: полное состояние компонента одним объектом.
	const (
		switchStatus = `{"id":0,"source":"WS_in","output":true,"apower":78.4,` +
			`"voltage":234.1,"current":0.34,"pf":0.98,"freq":50,` +
			`"aenergy":{"total":1234.567,"by_minute":[0,0,0],"minute_ts":1654604045},` +
			`"temperature":{"tC":44.4,"tF":111.9}}`

		coverStatus = `{"id":0,"source":"limit_switch","state":"open","apower":0,` +
			`"voltage":233,"current":0,"pf":0,"freq":50,` +
			`"aenergy":{"total":48.996,"by_minute":[0,0,0],"minute_ts":1654604045},` +
			`"temperature":{"tC":55.4,"tF":131.7},"pos_control":true,` +
			`"last_direction":"open","current_pos":100}`

		inputStatus = `{"id":0,"state":true}`

		// Нагрузка Tasmota: измерения и состояние приходят разными
		// сообщениями на разные топики, поэтому и объекты разные.
		tasmotaSensor = `{"Time":"2018.02.04 23:17:01","ENERGY":{"Total":3.185,` +
			`"Yesterday":3.058,"Today":0.127,"Power":78,"Factor":0.97,` +
			`"Voltage":221,"Current":0.334}}`

		tasmotaState = `{"Time":"2018.02.15 01:00:50","Uptime":"1 02:33:26",` +
			`"POWER":"OFF","Wifi":{"AP":1,"SSId":"XXX","RSSI":72,"Signal":-64}}`

		tasmotaTH = `{"Time":"2018.02.01 22:52:09",` +
			`"AM2301":{"Temperature":15.5,"Humidity":50.6},"TempUnit":"C"}`

		// Нагрузка Zigbee2MQTT: всё состояние устройства одним объектом.
		z2mPlug = `{"state":"ON","power":78,"voltage":230,"current":0.34,` +
			`"energy":1.234,"linkquality":60}`

		z2mSensor = `{"temperature":27.34,"humidity":44.72,"battery":87,` +
			`"voltage":3000,"linkquality":60}`
	)

	for _, tc := range []struct {
		profile string
		link    string // имя связки в профиле
		payload string // что пришло с шины
		want    string // что уедет в умный дом
		hex     string // байты на проводе; пусто — не проверяем
	}{
		// Реле с измерением: единицы должны совпасть с общим видом —
		// ватты и вольты целыми, энергия в киловаттах с одним знаком.
		{"shellyplus1pm", "Канал — состояние", switchStatus, "1", "01"},
		{"shellyplus1pm", "Мощность канала 0", switchStatus, "78 Вт", ""},
		{"shellyplus1pm", "Напряжение сети", switchStatus, "234 В", ""},
		{"shellyplus1pm", "Ток", switchStatus, "0.34 А", ""},
		// 1234.567 ватт-часа — это 1.2 киловатта после округления.
		{"shellyplus1pm", "Энергия канала 0", switchStatus, "1.2 кВт", ""},
		// Датчик уезжает как fixed-point 8.8, а не текстом.
		{"shellyplus1pm", "Температура устройства", switchStatus, "44.4", "66 2c"},
		{"shellyplus1pm", "Вход 0", inputStatus, "1", "01"},

		// Ролета: положение 0–100 ложится в один байт элемента штор.
		{"shellyplus2pm-roller", "Положение привода", coverStatus, "100", "64"},
		{"shellyplus2pm-roller", "Состояние привода", coverStatus, "open", ""},
		{"shellyplus2pm-roller", "Мощность", coverStatus, "0 Вт", ""},

		// Второй канал двухканального реле берёт свой компонент, а не нулевой.
		{"shellyplus2pm-relay", "Канал 1 — состояние",
			`{"id":1,"output":false,"apower":0,"voltage":234.1,"current":0,` +
				`"aenergy":{"total":0},"temperature":{"tC":40}}`, "0", "00"},

		// Датчик на батарейках: свои компоненты, свои имена полей.
		{"shellyplusht", "Температура", `{"id":0,"tC":23.5,"tF":74.3}`, "23.5", "80 17"},
		{"shellyplusht", "Влажность", `{"id":0,"rh":45.2}`, "45.2", "33 2d"},
		{"shellyplusht", "Заряд батареи",
			`{"id":0,"battery":{"V":6.01,"percent":87},"external":{"present":false}}`,
			"87 %", ""},

		// Завещание брокеру — единственный топик Gen2 без JSON.
		{"shellyplus1", "На связи", "true", "1", "01"},

		// Tasmota. Состояние приходит простым словом на stat, а не объектом:
		// это отдельный топик, и разбирать там нечего.
		{"tasmota-relay1", "Канал — состояние", "ON", "1", "01"},
		{"tasmota-relay1", "Канал — состояние", "OFF", "0", "00"},
		{"tasmota-relay4", "Канал 3 — состояние", "ON", "1", "01"},
		{"tasmota-relay1", "На связи", "Online", "1", "01"},
		{"tasmota-relay1", "На связи", "Offline", "0", "00"},

		// Измерения — объектом на tele, ключи вложены в ENERGY.
		// Total у Tasmota уже в киловатт-часах, множителя быть не должно.
		{"tasmota-plug", "Мощность", tasmotaSensor, "78 Вт", ""},
		{"tasmota-plug", "Напряжение сети", tasmotaSensor, "221 В", ""},
		{"tasmota-plug", "Ток", tasmotaSensor, "0.33 А", ""},
		{"tasmota-plug", "Энергия всего", tasmotaSensor, "3.2 кВт", ""},
		{"tasmota-plug", "Энергия за сегодня", tasmotaSensor, "0.1 кВт", ""},
		{"tasmota-plug", "Уровень сигнала WiFi", tasmotaState, "72 %", ""},

		// У датчиков ключ в сообщении — имя чипа, и перепутать его легко.
		{"tasmota-th", "Температура", tasmotaTH, "15.5", "80 0f"},
		{"tasmota-th", "Влажность", tasmotaTH, "50.6", "99 32"},
		{"tasmota-ds18b20", "Температура",
			`{"Time":"2018.02.01 21:29:40","DS18B20":{"Temperature":19.7},"TempUnit":"C"}`,
			"19.7", "b3 13"},

		// Zigbee2MQTT: всё состояние устройства одним объектом на один топик.
		{"z2m-plug", "Розетка — состояние", z2mPlug, "1", "01"},
		{"z2m-plug", "Мощность", z2mPlug, "78 Вт", ""},
		{"z2m-plug", "Напряжение сети", z2mPlug, "230 В", ""},
		{"z2m-plug", "Ток", z2mPlug, "0.34 А", ""},
		{"z2m-plug", "Энергия", z2mPlug, "1.2 кВт", ""},
		{"z2m-temp-hum", "Температура", z2mSensor, "27.34", "57 1b"},
		{"z2m-temp-hum", "Влажность", z2mSensor, "44.72", "b8 2c"},
		{"z2m-temp-hum", "Заряд батареи", z2mSensor, "87 %", ""},
		{"z2m-motion", "Движение", `{"occupancy":true,"illuminance":152}`, "1", "01"},
		{"z2m-motion", "Освещённость", `{"occupancy":true,"illuminance":152}`, "152 лк", ""},
		{"z2m-water-leak", "Протечка", `{"water_leak":true,"battery":100}`, "1", "01"},
		{"z2m-button", "Нажатие", `{"action":"double","battery":100}`, "1", "01"},

		// Поле contact у zigbee2mqtt устроено наоборот: true означает
		// «закрыто». Если перевернуть его обратно, датчик открытия покажет
		// открытую дверь закрытой — и наоборот.
		{"z2m-contact", "Открытие", `{"contact":false,"battery":100}`, "1", "01"},
		{"z2m-contact", "Открытие", `{"contact":true,"battery":100}`, "0", "00"},

		// Доступность здесь тоже объект, а не слово.
		{"z2m-switch", "На связи", `{"state":"online"}`, "1", "01"},
		{"z2m-switch", "На связи", `{"state":"offline"}`, "0", "00"},
	} {
		t.Run(tc.profile+"/"+tc.link, func(t *testing.T) {
			l := linkFromProfile(t, tc.profile, tc.link)

			wire, err := l.ToWire([]byte(tc.payload))
			if err != nil {
				t.Fatalf("ToWire: %v", err)
			}
			if wire.Text != tc.want {
				t.Errorf("значение %q, ожидалось %q", wire.Text, tc.want)
			}
			if tc.hex != "" && wire.Hex() != tc.hex {
				t.Errorf("байты %q, ожидалось %q", wire.Hex(), tc.hex)
			}
			if wire.Clamped {
				t.Errorf("значение обрезано под диапазон протокола")
			}
		})
	}
}

// Команды Gen2 уходят не словом, а объектом JSON-RPC, и собирается он таблицей
// значений. Ошибка в имени метода тоже тихая: устройство молча не отвечает.
func TestGen2CommandsAreRPC(t *testing.T) {
	for _, tc := range []struct {
		profile string
		link    string
		state   byte   // что пришло от элемента умного дома
		method  string // какой метод должен оказаться в нагрузке
	}{
		{"shellyplus1pm", "Канал — команда", 1, "Switch.Set"},
		{"shellyplus1pm", "Канал — команда", 0, "Switch.Set"},
		// Нажатие в приложении приходит как 0xFF и означает «переключи».
		// У Gen2 для этого есть свой метод — гадать о текущем состоянии не надо.
		{"shellyplus1pm", "Канал — команда", 0xFF, "Switch.Toggle"},
		{"shellyplus2pm-roller", "Команда приводу", 0, "Cover.Close"},
		{"shellyplus2pm-roller", "Команда приводу", 100, "Cover.Open"},
	} {
		t.Run(tc.profile+"/"+tc.method, func(t *testing.T) {
			l := linkFromProfile(t, tc.profile, tc.link)

			payload, err := l.ToPayload([]byte{tc.state})
			if err != nil {
				t.Fatalf("ToPayload: %v", err)
			}
			if !strings.Contains(payload, tc.method) {
				t.Errorf("нагрузка %q, ожидался метод %s", payload, tc.method)
			}
			if !strings.HasPrefix(payload, "{") {
				t.Errorf("нагрузка %q не объект JSON", payload)
			}
		})
	}
}

// У Tasmota команда — одно слово, и переключение она понимает сама. Это и
// делает её самой удобной прошивкой для нас: нажатие 0xFF уходит как TOGGLE,
// и знать текущее состояние реле шлюзу не нужно.
func TestTasmotaCommandsArePlainWords(t *testing.T) {
	for _, tc := range []struct {
		state byte
		want  string
	}{
		{1, "ON"},
		{8, "ON"},
		{0, "OFF"},
		{9, "OFF"},
		{0xFF, "TOGGLE"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			l := linkFromProfile(t, "tasmota-relay1", "Канал — команда")

			payload, err := l.ToPayload([]byte{tc.state})
			if err != nil {
				t.Fatalf("ToPayload: %v", err)
			}
			if payload != tc.want {
				t.Errorf("нагрузка %q, ожидалось %q", payload, tc.want)
			}
		})
	}
}

// Zigbee2MQTT принимает команду объектом, а переключение понимает само —
// как и Tasmota, текущее состояние знать не нужно.
func TestZ2MCommandsAreJSON(t *testing.T) {
	for _, tc := range []struct {
		state byte
		want  string
	}{
		{1, `{"state":"ON"}`},
		{0, `{"state":"OFF"}`},
		{0xFF, `{"state":"TOGGLE"}`},
	} {
		t.Run(tc.want, func(t *testing.T) {
			l := linkFromProfile(t, "z2m-switch", "Выключатель — команда")

			payload, err := l.ToPayload([]byte{tc.state})
			if err != nil {
				t.Fatalf("ToPayload: %v", err)
			}
			if payload != tc.want {
				t.Errorf("нагрузка %q, ожидалось %q", payload, tc.want)
			}
		})
	}
}

// linkFromProfile разворачивает профиль и достаёт из него связку по имени.
//
// Роли назначаются все подряд условными адресами: какой именно элемент за
// ролью, для разбора нагрузки безразлично, а без назначения связка выпала бы
// из результата.
func linkFromProfile(t *testing.T, key, name string) link.Link {
	t.Helper()

	tmpl, err := Find(key)
	if err != nil {
		t.Fatalf("профиль %s: %v", key, err)
	}

	assign := make(map[string]Addr, len(tmpl.Roles))
	for i, r := range tmpl.Roles {
		assign[r.Key] = Addr{ID: uint16(100 + i), SubID: 1}
	}

	links, err := tmpl.Apply("shellyplus1pm-a8032abd1eb4", assign)
	if err != nil {
		t.Fatalf("применение профиля %s: %v", key, err)
	}

	for _, l := range links {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("в профиле %s нет связки «%s»", key, name)
	return link.Link{}
}
