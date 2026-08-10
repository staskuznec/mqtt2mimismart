// Package config описывает YAML-конфиг бинарника Shelly_2.5.
//
// Конфиг решает две задачи: где искать сервер MimiSmart и на какие
// id/subid отдавать состояние конкретного канала конкретного реле.
// Умный дом передаёт в бинарник только "ip|action|channel", поэтому вся
// привязка «реле → точки умного дома» живёт здесь.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultFileName — имя конфига, которое ищется рядом с бинарником,
// если не задан путь через переменную окружения SHELLY_CONFIG.
const DefaultFileName = "94.yaml"

// EnvConfigPath — переменная окружения с путём к конфигу.
const EnvConfigPath = "SHELLY_CONFIG"

// Config — корень YAML-файла.
type Config struct {
	Mimismart Mimismart `yaml:"mimismart"`
	Logging   Logging   `yaml:"logging,omitempty"`
	Runtime   Runtime   `yaml:"runtime,omitempty"`
	Devices   []Device  `yaml:"devices"`
}

// Mimismart — параметры подключения shClient к серверу умного дома.
type Mimismart struct {
	Addr string `yaml:"addr"` // host:port сервера SHS
	Key  string `yaml:"key"`  // ключ AES (16/24/32 байта)
	Mac  string `yaml:"mac"`  // mac-id клиента; пустой — сгенерируется случайный
}

// Logging — настройки zerolog.
type Logging struct {
	Level  string `yaml:"level"`  // debug | info | warn | error (по умолчанию error)
	Format string `yaml:"format"` // text | json
}

// EnvDryRun — переменная окружения для разового сухого прогона.
// Удобна для тестов: не надо править конфиг.
const EnvDryRun = "SHELLY_DRY_RUN"

// Runtime — общие параметры запуска.
type Runtime struct {
	TimeoutSec  int   `yaml:"timeout_sec,omitempty"`         // общий watchdog процесса
	HTTPTimeout int   `yaml:"http_timeout_sec,omitempty"`    // таймаут запроса к реле
	ReportAfter *bool `yaml:"report_after_action,omitempty"` // отдавать состояние после on/off/toggle
	Dry         bool  `yaml:"dry,omitempty"`                 // не отправлять в умный дом, только показать

	// VerifyDelayMs — пауза перед перечитыванием состояния канала после
	// команды. Нужна только когда включён report_after_action; по умолчанию
	// состояние после команды не отправляется вовсе, и проверка не работает.
	VerifyDelayMs *int `yaml:"verify_delay_ms,omitempty"`

	// OnlyChangedValues — отправлять только изменившиеся значения.
	// По умолчанию включено: иначе по таймеру каждую минуту уходят одни и те
	// же числа, а обработчик лампы в скрипте дёргается вхолостую.
	OnlyChangedValues *bool `yaml:"only_changed,omitempty"`

	// ResendAfterSec — как часто неизменившееся значение всё же отправляется
	// повторно. Страховка от рассинхрона, если элемент поменяли мимо нас.
	ResendAfterSec *int `yaml:"resend_after_sec,omitempty"`
}

// DryRun сообщает, включён ли сухой прогон: реле опрашивается и переключается
// по-настоящему, но значения в умный дом не уходят, а печатаются в stdout.
// Включается в конфиге (dry: true) или переменной окружения SHELLY_DRY_RUN.
func (r Runtime) DryRun() bool {
	if r.Dry {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvDryRun))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// ReportAfterAction сообщает, нужно ли после команды отправлять новое
// состояние в умный дом. По умолчанию — НЕТ.
//
// Единственный источник состояния — периодический опрос. Если отвечать ещё и
// сразу после команды, получаются две записи подряд: одна от команды, вторая
// от опроса, — и при малейшем расхождении элемент в интерфейсе дёргается.
// Опрос идёт раз в несколько секунд, так что задержка незаметна.
func (r Runtime) ReportAfterAction() bool {
	if r.ReportAfter == nil {
		return false
	}
	return *r.ReportAfter
}

// VerifyDelay возвращает паузу перед перечитыванием состояния после команды.
// По умолчанию 300 мс — этого хватает, чтобы реле успело отработать.
func (r Runtime) VerifyDelay() time.Duration {
	if r.VerifyDelayMs == nil {
		return 300 * time.Millisecond
	}
	if *r.VerifyDelayMs <= 0 {
		return 0
	}
	return time.Duration(*r.VerifyDelayMs) * time.Millisecond
}

// OnlyChanged сообщает, нужно ли фильтровать неизменившиеся значения.
// По умолчанию — НЕТ: опрос отдаёт состояние устройства целиком каждый раз,
// менялось оно или нет. Так элемент в умном доме гарантированно совпадает с
// реле, даже если запись когда-то потерялась.
func (r Runtime) OnlyChanged() bool {
	if r.OnlyChangedValues == nil {
		return false
	}
	return *r.OnlyChangedValues
}

// ResendAfter возвращает период принудительной повторной отправки.
func (r Runtime) ResendAfter() time.Duration {
	if r.ResendAfterSec == nil || *r.ResendAfterSec <= 0 {
		return 0 // пакет подставит значение по умолчанию
	}
	return time.Duration(*r.ResendAfterSec) * time.Second
}

// Point — адрес значения в умном доме (id элемента + subid).
//
// Scale — множитель, применяемый к значению перед отправкой. Он нужен из-за
// формата протокола: значение уходит как fixed-point 8.8 (uint16 = v*256),
// то есть представимый диапазон всего 0…255.99. Мощность в ваттах туда не
// влезает, поэтому для неё задают, например, scale: 0.1 (значение в десятках
// ватт) или 0.001 (киловатты). Пустой scale означает 1.
type Point struct {
	ID    uint16  `yaml:"id"`
	SubID uint8   `yaml:"subid"`
	Scale float64 `yaml:"scale,omitempty"`

	// As задаёт, чем элемент объявлен в логике умного дома:
	//
	//	"sensor" — число в формате fixed-point 8.8, два байта (по умолчанию);
	//	"text"   — строка, длина произвольная;
	//	"byte"   — одно значение 0..255 ровно в одном байте.
	//
	// "byte" обязателен для элементов, состояние которых занимает один байт:
	// лампа, скрипт, клапан, шторы, ворота, датчики двери и протечки. Если
	// отправить такому элементу значение датчика, единица уедет как байты
	// 00 01, младший байт окажется нулём, и элемент останется выключенным.
	As string `yaml:"as,omitempty"`

	// Unit приписывается к текстовому значению, например " Вт".
	Unit string `yaml:"unit,omitempty"`

	// Precision — знаков после запятой в текстовом значении.
	Precision *int `yaml:"precision,omitempty"`
}

// Формы представления значения на проводе.
const (
	AsSensor = "sensor"
	AsText   = "text"
	AsByte   = "byte"
)

// kind возвращает форму представления с учётом значения по умолчанию.
func (p Point) kind() string {
	k := strings.ToLower(strings.TrimSpace(p.As))
	if k == "" {
		return AsSensor
	}
	return k
}

// AsText сообщает, что значение уходит текстом.
func (p Point) AsText() bool { return p.kind() == AsText }

// AsSingleByte сообщает, что значение уходит одним сырым байтом.
func (p Point) AsSingleByte() bool { return p.kind() == AsByte }

// WithByteDefault возвращает точку, у которой форма представления по
// умолчанию — один байт. Применяется к состояниям и флагам: они живут в
// однобайтовых элементах, а не в датчиках.
func (p Point) WithByteDefault() Point {
	if strings.TrimSpace(p.As) == "" {
		p.As = AsByte
	}
	return p
}

// Apply применяет масштабный коэффициент точки к значению.
func (p Point) Apply(v float64) float64 {
	if p.Scale == 0 {
		return v
	}
	return v * p.Scale
}

// Digits возвращает число знаков после запятой для текстового значения.
func (p Point) Digits() int {
	if p.Precision == nil {
		return 1
	}
	if *p.Precision < 0 {
		return 0
	}
	return *p.Precision
}

// Channel — привязка одного релейного канала Shelly к точкам умного дома.
// Обязателен только State, остальные точки опциональны.
type Channel struct {
	Channel   int    `yaml:"channel"`             // 0 (O1) или 1 (O2)
	Name      string `yaml:"name,omitempty"`      // для логов
	State     Point  `yaml:"state"`               // 1 (вкл) или 0 (выкл)
	Power     *Point `yaml:"power,omitempty"`     // мощность канала, Вт
	Energy    *Point `yaml:"energy,omitempty"`    // накопленная энергия, кВ
	Overpower *Point `yaml:"overpower,omitempty"` // 1 — сработала защита по мощности
}

// Device — одно реле Shelly 2.5.
type Device struct {
	IP              string    `yaml:"ip"`
	Name            string    `yaml:"name,omitempty"`
	User            string    `yaml:"user,omitempty"` // Basic Auth, если включён на реле
	Password        string    `yaml:"password,omitempty"`
	Channels        []Channel `yaml:"channels"`
	Temperature     *Point    `yaml:"temperature,omitempty"`     // температура устройства, °C
	Overtemperature *Point    `yaml:"overtemperature,omitempty"` // 1 — перегрев
}

// Load читает и валидирует конфиг.
// Путь берётся из SHELLY_CONFIG, иначе ищется рядом с исполняемым файлом.
func Load() (*Config, string, error) {
	path, err := resolvePath()
	if err != nil {
		return nil, "", err
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, path, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, path, fmt.Errorf("config %s: %w", path, err)
	}
	return &cfg, path, nil
}

// resolvePath определяет путь к конфигу.
//
// Порядок такой: явный путь из SHELLY_CONFIG, затем файл с именем самого
// бинарника и расширением .yaml рядом с ним, затем 94.yaml там же.
// Именование по бинарнику позволяет держать рядом несколько копий с разными
// настройками: 93.sh читает 93.yaml, shelly25 читает shelly25.yaml.
func resolvePath() (string, error) {
	if p := strings.TrimSpace(os.Getenv(EnvConfigPath)); p != "" {
		return filepath.Abs(p)
	}

	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("determine executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	dir := filepath.Dir(exe)
	base := filepath.Base(exe)
	// Снимаем расширение бинарника: 93.sh → 93, shelly25.exe → shelly25.
	named := filepath.Join(dir, strings.TrimSuffix(base, filepath.Ext(base))+".yaml")
	if _, err := os.Stat(named); err == nil {
		return named, nil
	}

	// Запасной вариант с общим именем — и он же попадёт в текст ошибки,
	// если конфига нет вовсе.
	fallback := filepath.Join(dir, DefaultFileName)
	if _, err := os.Stat(fallback); err == nil {
		return fallback, nil
	}
	return named, nil
}

// validate проверяет минимально необходимые поля и отсутствие дублей.
func (c *Config) validate() error {
	// В сухом прогоне соединение с умным домом не открывается,
	// поэтому его параметры можно не заполнять.
	if !c.Runtime.DryRun() {
		if c.Mimismart.Addr == "" {
			return fmt.Errorf("mimismart.addr не задан")
		}
		if c.Mimismart.Key == "" {
			return fmt.Errorf("mimismart.key не задан")
		}
	}
	// Секция devices теперь необязательна: адреса назначения и учётные данные
	// можно передавать прямо в команде. Если она есть — проверяем.
	seenIP := make(map[string]struct{}, len(c.Devices))
	for i, d := range c.Devices {
		if d.IP == "" {
			return fmt.Errorf("devices[%d]: пустой ip", i)
		}
		if _, dup := seenIP[d.IP]; dup {
			return fmt.Errorf("devices[%d]: ip %s повторяется", i, d.IP)
		}
		seenIP[d.IP] = struct{}{}

		if len(d.Channels) == 0 {
			return fmt.Errorf("devices[%d] (%s): не задано ни одного канала", i, d.IP)
		}
		seenCh := make(map[int]struct{}, len(d.Channels))
		for j, ch := range d.Channels {
			if ch.Channel != 0 && ch.Channel != 1 {
				return fmt.Errorf("devices[%d].channels[%d]: channel=%d, допустимы 0 и 1", i, j, ch.Channel)
			}
			if _, dup := seenCh[ch.Channel]; dup {
				return fmt.Errorf("devices[%d]: канал %d описан дважды", i, ch.Channel)
			}
			seenCh[ch.Channel] = struct{}{}
		}
	}
	return nil
}

// FindDevice возвращает устройство по IP.
func (c *Config) FindDevice(ip string) (*Device, error) {
	for i := range c.Devices {
		if c.Devices[i].IP == ip {
			return &c.Devices[i], nil
		}
	}
	return nil, fmt.Errorf("устройство с ip %s не найдено в конфиге", ip)
}

// FindChannel возвращает описание канала устройства.
func (d *Device) FindChannel(ch int) (*Channel, error) {
	for i := range d.Channels {
		if d.Channels[i].Channel == ch {
			return &d.Channels[i], nil
		}
	}
	return nil, fmt.Errorf("канал %d не описан для устройства %s", ch, d.IP)
}
