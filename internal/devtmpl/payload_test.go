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

		// Снимок с живого объекта: Shelly 2.5 в режиме реле, топик info.
		// Имя сети, адреса и MAC заменены, остальное как пришло с шины.
		shelly25Info = `{"wifi_sta":{"connected":true,"ssid":"home",` +
			`"ip":"192.168.20.222","rssi":-70},"cloud":{"enabled":false,` +
			`"connected":false},"mqtt":{"connected":true},"time":"23:48",` +
			`"serial":1,"has_update":false,"mac":"000000000000",` +
			`"relays":[{"ison":false,"overpower":false,"is_valid":true,"source":"http"},` +
			`{"ison":true,"overpower":false,"is_valid":true,"source":"mqtt"}],` +
			`"meters":[{"power":0.00,"overpower":0.00,"is_valid":true,"total":3},` +
			`{"power":0.00,"overpower":0.00,"is_valid":true,"total":0}],` +
			`"temperature":53.87,"overtemperature":false,"tmp":{"tC":53.87,"is_valid":true}}`

		// Живой снимок с объекта: датчик протечки Shelly Flood, топик info.
		// Имя сети и адрес заменены. Уровень сигнала батарейный датчик кладёт
		// туда же, куда и реле, — в wifi_sta, а не в свой раздел с водой.
		shellyFloodInfo = `{"wifi_sta":{"connected":true,"ssid":"home",` +
			`"ip":"192.168.20.240","rssi":-51},"cloud":{"enabled":true,` +
			`"connected":false},"mqtt":{"connected":true},"time":"12:35",` +
			`"serial":1,"has_update":false,"mac":"000000000000",` +
			`"is_valid":true,"flood":false,` +
			`"tmp":{"value":22.5,"units":"C","tC":22.5,"is_valid":true},` +
			`"bat":{"value":100,"voltage":2.96},"act_reasons":["sensor"]}`

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

		// Завещание брокеру — единственный топик Gen2 без JSON. Элемент
		// текстовый: «офлайн» на панели читается сразу, ноль в лампе —
		// только если помнить, что эта лампа означает.
		{"shellyplus1", "На связи", "true", "онлайн", ""},
		{"shellyplus1", "На связи", "false", "офлайн", ""},

		// Уровень сигнала WiFi: точного совпадения у dBm не бывает, слово
		// выбирается порогом. Gen2 публикует его в status/wifi.
		{"shellyplus1", "Уровень сигнала WiFi",
			`{"sta_ip":"192.168.1.44","status":"got ip","ssid":"home","rssi":-58}`, "хороший", ""},
		{"shellyplus1", "Уровень сигнала WiFi",
			`{"sta_ip":"192.168.1.44","status":"got ip","ssid":"home","rssi":-74}`, "нормальный", ""},
		{"shellyplus1", "Уровень сигнала WiFi",
			`{"sta_ip":"192.168.1.44","status":"got ip","ssid":"home","rssi":-88}`, "плохой", ""},

		// Живой снимок с объекта: топик info Shelly 2.5 в режиме реле. Поля
		// идут в том же порядке, что и на устройстве, — по нему видно, что
		// wifi_sta.rssi лежит первым уровнем вложенности, а не в статусе
		// каждого реле.
		{"shelly25-relay", "Уровень сигнала WiFi", shelly25Info, "нормальный", ""},
		{"shelly25-relay", "Температура устройства", "53.87", "53.87", "de 35"},

		// Gen1 кладёт то же самое в полный статус на топике info, полем
		// wifi_sta.rssi. Реле, которое подмигивает, обычно живёт как раз на
		// границе: -80 dBm — это «плохой», и видно это только словом.
		{"shelly25-relay", "Уровень сигнала WiFi",
			`{"wifi_sta":{"connected":true,"ssid":"home","ip":"192.168.1.51","rssi":-80},` +
				`"uptime":94021,"has_update":false}`, "плохой", ""},
		{"shelly25-relay", "Уровень сигнала WiFi",
			`{"wifi_sta":{"connected":true,"ssid":"home","ip":"192.168.1.51","rssi":-73},` +
				`"uptime":94021,"has_update":false}`, "нормальный", ""},
		{"shelly25-relay", "На связи", "false", "офлайн", ""},

		// Батарейные датчики публикуют info при пробуждении, и это
		// единственный момент, когда об их сигнале вообще можно узнать:
		// «на связи» у спящего устройства бессмысленно, а слабый сигнал —
		// ровно то, из-за чего показания перестают доходить.
		{"shellyflood", "Уровень сигнала WiFi", shellyFloodInfo, "хороший", ""},
		// Живой список топиков датчика протечки: всё, кроме online и info,
		// он публикует под sensor/. Профиль до этого ходил за флудом и
		// температурой в корень префикса и молчал вовсе — на объекте это
		// выглядело как «датчик не работает».
		{"shellyflood", "Протечка", "true", "1", "01"},
		{"shellyflood", "Протечка", "false", "0", "00"},
		{"shellyflood", "Температура", "24.88", "24.88", "e1 18"},
		{"shellyflood", "Заряд батареи", "71", "71", "00 47"},
		{"shellyflood", "Исправность датчика", "0", "исправен", ""},
		{"shellyflood", "Исправность датчика", "1", "ошибка датчика", ""},
		// Причина пробуждения приходит массивом из одного слова. По нему и
		// видно, что датчик живой: спящее устройство иначе неотличимо от
		// снятого со стены.
		{"shellyflood", "Отчего проснулся", `["button"]`, "кнопка", ""},
		{"shellyflood", "Отчего проснулся", `["sensor"]`, "вода", ""},
		{"shellyflood", "На связи", "false", "офлайн", ""},
		{"shellyht", "Уровень сигнала WiFi",
			`{"wifi_sta":{"connected":true,"ssid":"home","ip":"192.168.1.60","rssi":-84},` +
				`"bat":{"value":94,"voltage":2.93},"tmp":{"value":21.5,"is_valid":true}}`,
			"плохой", ""},
		// Gen2 на батарейках берёт уровень из своего status/wifi, как и
		// остальные Plus, — общего для двух поколений топика нет.
		{"shellyplusht", "Уровень сигнала WiFi",
			`{"sta_ip":"192.168.1.61","status":"got ip","ssid":"home","rssi":-73}`,
			"нормальный", ""},

		// Защита по мощности выключает канал и публикует это словом вместо
		// «off». Единица в элементе означала бы горящую лампу при выбитом
		// реле — то есть шлюз врал бы ровно в тот момент, когда разбираются,
		// почему свет не включается.
		{"shelly25-relay", "Канал 0 — состояние", "overpower", "0", "00"},
		{"shelly1pm", "Канал 0 — состояние", "overpower", "0", "00"},

		// Tasmota. Состояние приходит простым словом на stat, а не объектом:
		// это отдельный топик, и разбирать там нечего.
		{"tasmota-relay1", "Канал — состояние", "ON", "1", "01"},
		{"tasmota-relay1", "Канал — состояние", "OFF", "0", "00"},
		{"tasmota-relay4", "Канал 3 — состояние", "ON", "1", "01"},
		{"tasmota-relay1", "На связи", "Online", "онлайн", ""},
		{"tasmota-relay1", "На связи", "Offline", "офлайн", ""},

		// Уровень сигнала — из того же tele/STATE, что и состояние: поле
		// Wifi.Signal, dBm. Проценты рядом в Wifi.RSSI, но пороги считаем по
		// dBm, как у Shelly, — одна шкала на весь объект.
		{"tasmota-relay1", "Уровень сигнала WiFi", tasmotaState, "хороший", ""},

		// Измерения — объектом на tele, ключи вложены в ENERGY.
		// Total у Tasmota уже в киловатт-часах, множителя быть не должно.
		{"tasmota-plug", "Мощность", tasmotaSensor, "78 Вт", ""},
		{"tasmota-plug", "Напряжение сети", tasmotaSensor, "221 В", ""},
		{"tasmota-plug", "Ток", tasmotaSensor, "0.33 А", ""},
		{"tasmota-plug", "Энергия всего", tasmotaSensor, "3.2 кВт", ""},
		{"tasmota-plug", "Энергия за сегодня", tasmotaSensor, "0.1 кВт", ""},
		// Проценты из Wifi.RSSI сменились словом по Wifi.Signal: у dBm та же
		// шкала, что у Shelly, и на объекте с обеими прошивками это читается
		// как одно показание, а не два разных.
		{"tasmota-plug", "Уровень сигнала WiFi", tasmotaState, "хороший", ""},

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
		{"z2m-switch", "На связи", `{"state":"online"}`, "онлайн", ""},
		{"z2m-switch", "На связи", `{"state":"offline"}`, "офлайн", ""},

		// Zigbee меряет связь не в dBm, а в LQI: чем больше, тем лучше,
		// поэтому пороги идут «больше либо равно». 60 — это середина.
		{"z2m-plug", "Уровень сигнала Zigbee", z2mPlug, "нормальный", ""},
		{"z2m-temp-hum", "Уровень сигнала Zigbee", z2mSensor, "нормальный", ""},
		{"z2m-plug", "Уровень сигнала Zigbee",
			`{"state":"ON","power":78,"linkquality":140}`, "хороший", ""},
		{"z2m-plug", "Уровень сигнала Zigbee",
			`{"state":"ON","power":78,"linkquality":18}`, "плохой", ""},

		// МикроАрт: агент раскладывает поля API по отдельным топикам, и в
		// каждом лежит голое число строкой — разбирать нечего.
		{"microart-map", "Заряд АКБ", "88.0", "88 %", ""},
		{"microart-map", "Остаточная ёмкость", "178.4", "178 А·ч", ""},
		{"microart-map", "Напряжение сети", "231.0", "231 В", ""},
		{"microart-map", "Мощность нагрузки", "1740.0", "1740 Вт", ""},
		// Ток АКБ и мощность по сети приходят со знаком: минус — разряд и
		// отдача в сеть. Показанием датчика такое уехало бы нулём.
		{"microart-map", "Ток АКБ", "-12.5", "-12.50 А", ""},
		{"microart-map", "Мощность по сети", "-1350.0", "-1350 Вт", ""},
		// Выработка за сутки считается в ватт-часах — в киловатты её
		// переводит множитель, как у Shelly ватт-минуты.
		{"microart-map", "Выработка солнца за сутки", "3520", "3.5 кВт", ""},
		// Напряжение и температура АКБ уезжают показанием датчика: десятые
		// вольта отличают заряженную батарею от севшей.
		{"microart-map", "Напряжение АКБ", "52.4", "52.4", "66 34"},
		{"microart-map", "Температура АКБ", "24.5", "24.5", "80 18"},
		// Режим и статус приходят числовыми кодами, а не словами: расшифровка
		// в документации МикроАрт, и подменять её таблицей значений нельзя —
		// у прошивок она разная.
		{"microart-map", "Режим МАП", "3", "3", ""},
		{"microart-map", "Статус работы МАП", "2", "2", ""},
		{"microart-map", "Частота сети", "50.0", "50.0 Гц", ""},
		{"microart-map", "Ток сети", "2.9", "2.90 А", ""},
		{"microart-map", "Напряжение на выходе", "221", "221 В", ""},
		{"microart-map", "Ёмкость АКБ C20", "200.0", "200 А·ч", ""},
		{"microart-map", "Температура МАП, датчик 3", "27", "27", "00 1b"},
		// Заряд и доступность идут дважды: текстом на экран и показанием в
		// логику. Элемент умного дома бывает и таким, и таким.
		{"microart-map", "Заряд АКБ показанием", "86.3", "86.3", "4c 56"},
		{"microart-map", "На связи показанием", "online", "1", "00 01"},
		// Словом — как пришло с шины: в текстовом элементе человеку нужна
		// надпись, а не единица, которую ещё надо помнить как читать.
		{"microart-map", "На связи словом", "online", "online", ""},
		{"microart-map", "На связи словом", "offline", "offline", ""},
		{"microart-map", "На связи показанием", "offline", "0", "00 00"},
		// Ток заряда и ток разряда — один топик, разный знак множителя.
		// Каждый показывает свою половину, чужую обрезает в ноль, и сценарий
		// отличает заряд от разряда сравнением, а не арифметикой.
		{"microart-map", "Ток заряда АКБ", "12.5", "12.5", "80 0c"},
		{"microart-map", "Ток разряда АКБ", "-12.5", "12.5", "80 0c"},
		// Инвертор без монитора батарей раздела bat не публикует вовсе, и
		// ток АКБ там остаётся только в map — целыми амперами.
		{"microart-map", "Ток заряда АКБ по данным МАП", "6", "6", "00 06"},
		{"microart-map", "Номер инвертора", "34396", "34396", ""},
		// Тот же МАП, но без монитора батарей: раздела bat нет, и всё, что
		// есть, лежит в map. Отдельный профиль ровно по этому набору.
		{"microart-hybrid", "Напряжение АКБ", "49.9", "49.9", "e6 31"},
		{"microart-hybrid", "Ток заряда АКБ", "6", "6", "00 06"},
		{"microart-hybrid", "Ток разряда АКБ", "-6", "6", "00 06"},
		{"microart-hybrid", "Напряжение сети", "225", "225 В", ""},
		{"microart-hybrid", "Мощность по сети", "190", "190 Вт", ""},
		{"microart-hybrid", "Частота сети", "50.0", "50.0 Гц", ""},
		{"microart-hybrid", "Режим ЭКО", "1", "1", ""},
		{"microart-hybrid", "Номер инвертора", "34396", "34396", ""},
		{"microart-hybrid", "На связи словом", "offline", "offline", ""},
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

// Ток АКБ у МАП один и приходит со знаком, а показание датчика знака не имеет.
// Профиль делит его на две величины: заряд и разряд, каждая своей связкой с
// обратным множителем. Половина, которой сейчас нет, обязана быть ровно нулём —
// иначе сценарий «заряжается» срабатывал бы и при разряде.
func TestMicroartSplitsChargeAndDischarge(t *testing.T) {
	charge := linkFromProfile(t, "microart-map", "Ток заряда АКБ")
	discharge := linkFromProfile(t, "microart-map", "Ток разряда АКБ")

	for _, tc := range []struct {
		payload string
		charge  string
		dischg  string
	}{
		{"12.50", "12.5", "0"},
		{"-12.50", "0", "12.5"},
		{"0.00", "0", "0"},
	} {
		t.Run(tc.payload, func(t *testing.T) {
			for _, side := range []struct {
				name string
				link link.Link
				want string
			}{
				{"заряд", charge, tc.charge},
				{"разряд", discharge, tc.dischg},
			} {
				wire, err := side.link.ToWire([]byte(tc.payload))
				if err != nil {
					t.Fatalf("%s: ToWire: %v", side.name, err)
				}
				if wire.Text != side.want {
					t.Errorf("%s = %q, ожидалось %q", side.name, wire.Text, side.want)
				}
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
