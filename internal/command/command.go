// Package command разбирает аргумент, который умный дом передаёт бинарнику.
//
// Умный дом кладёт в один из аргументов HEX-строку. После декодирования
// получается текст вида "host|action|channel[|цель=id:subid]...", возможно с
// null-терминатором:
//
//	"192.168.20.222|on|0"
//	"192.168.20.222|toggle|1"
//	"192.168.20.222|status|0|state=563:111|power=563:113"
//	"shadow:secret@192.168.20.222|status|0|state=563:111|temp=563:115"
//	"192.168.20.222|status|"                       — все каналы, привязка из конфига
//
// Адрес устройства может содержать учётные данные Basic Auth в форме
// "user:pass@ip" — тогда прописывать их в конфиге не нужно.
//
// Цели (куда писать результат) перечисляются парами "имя=id:subid". Если их
// не передали, привязка берётся из конфига по IP устройства.
package command

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// EnvArgIndex позволяет жёстко закрепить номер аргумента с командой,
// если автоопределение по какой-то причине не подходит.
// Значение — индекс в os.Args: "2" это второй аргумент, "3" — третий.
const EnvArgIndex = "SHELLY_ARG_INDEX"

// Action — что бинарник должен сделать с реле.
type Action string

const (
	ActionOn     Action = "on"
	ActionOff    Action = "off"
	ActionToggle Action = "toggle"
	ActionStatus Action = "status"
)

// Имена целей, которые можно передать в команде.
//
// Первые четыре относятся к каналу, и к ним можно приписать его номер:
// "state0", "power1". Тогда одна команда покрывает оба канала сразу, а номер
// канала в самой команде можно не указывать. Без номера цель относится к тому
// каналу, что указан в команде.
//
// Остальные описывают устройство целиком и номера не имеют: напряжение сети
// одно на оба выхода, температура и перегрев — тоже общие.
const (
	TargetState     = "state"     // состояние реле, 1 или 0
	TargetPower     = "power"     // мощность канала, Вт
	TargetEnergy    = "energy"    // накопленная энергия канала, кВ
	TargetOverpower = "overpower" // 1 — сработала защита по мощности

	TargetVoltage  = "voltage"  // напряжение сети, В
	TargetTemp     = "temp"     // температура устройства, °C
	TargetOvertemp = "overtemp" // 1 — перегрев устройства
)

// channelTargets — цели, относящиеся к каналу.
var channelTargets = []string{TargetState, TargetPower, TargetEnergy, TargetOverpower}

// deviceTargets — цели, относящиеся к устройству целиком.
var deviceTargets = []string{TargetVoltage, TargetTemp, TargetOvertemp}

// AllChannels — значение Channel, когда канал в команде не указан.
const AllChannels = -1

// Addr — адрес элемента умного дома.
type Addr struct {
	ID    uint16
	SubID uint8
}

func (a Addr) String() string { return fmt.Sprintf("%d:%d", a.ID, a.SubID) }

// Command — разобранная команда.
type Command struct {
	Host     string // адрес реле, возможно с "user:pass@"
	IP       string // тот же адрес без учётных данных
	User     string // логин Basic Auth, если был в адресе
	Password string // пароль Basic Auth, если был в адресе
	Action   Action
	Channel  int             // 0, 1 или AllChannels
	Targets  map[string]Addr // куда писать результат; пусто — брать из конфига
	Raw      string
}

// IsAllChannels сообщает, что команда относится ко всем каналам устройства.
func (c Command) IsAllChannels() bool { return c.Channel == AllChannels }

// HasTargets сообщает, что адреса назначения переданы прямо в команде.
func (c Command) HasTargets() bool { return len(c.Targets) > 0 }

// Target возвращает адрес цели устройства (voltage, temp, overtemp).
func (c Command) Target(name string) (Addr, bool) {
	a, ok := c.Targets[name]
	return a, ok
}

// ChannelTarget возвращает адрес цели для конкретного канала.
// Сначала ищется имя с номером ("power1"), затем без него ("power") —
// но безномерное имя относится только к каналу из самой команды.
func (c Command) ChannelTarget(name string, ch int) (Addr, bool) {
	if a, ok := c.Targets[name+strconv.Itoa(ch)]; ok {
		return a, true
	}
	if ch == c.Channel {
		if a, ok := c.Targets[name]; ok {
			return a, true
		}
	}
	return Addr{}, false
}

// TargetChannels перечисляет каналы, для которых в команде есть адреса.
func (c Command) TargetChannels() []int {
	var channels []int
	for _, ch := range []int{0, 1} {
		for _, name := range channelTargets {
			if _, ok := c.ChannelTarget(name, ch); ok {
				channels = append(channels, ch)
				break
			}
		}
	}
	return channels
}

// FromArgs находит среди аргументов запуска тот, что содержит HEX-команду,
// и разбирает его. Возвращает также индекс использованного аргумента.
//
// Разные конфигурации умного дома передают команду в разной позиции, поэтому
// позиция не зашита: аргументы просматриваются слева направо, и берётся
// первый, который декодируется из HEX и разбирается в осмысленную команду.
//
// Если автоопределение не подходит, номер аргумента жёстко задаётся
// переменной окружения SHELLY_ARG_INDEX.
func FromArgs(args []string) (*Command, int, error) {
	if idx, ok := pinnedIndex(); ok {
		if idx < 1 || idx >= len(args) {
			return nil, 0, fmt.Errorf("%s=%d, но аргумента с таким номером нет (передано %d)",
				EnvArgIndex, idx, len(args)-1)
		}
		cmd, err := Parse(args[idx])
		if err != nil {
			return nil, 0, err
		}
		return cmd, idx, nil
	}

	var firstErr error
	for i := 1; i < len(args); i++ {
		cmd, err := Parse(args[i])
		if err == nil {
			return cmd, i, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}

	if firstErr == nil {
		return nil, 0, fmt.Errorf("не передано ни одного аргумента с командой")
	}
	return nil, 0, fmt.Errorf("ни один из аргументов не содержит HEX-команду вида host|action|channel: %w", firstErr)
}

// pinnedIndex читает жёстко заданный номер аргумента из окружения.
func pinnedIndex() (int, bool) {
	raw := strings.TrimSpace(os.Getenv(EnvArgIndex))
	if raw == "" {
		return 0, false
	}
	idx, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return idx, true
}

// Parse декодирует HEX-строку и разбирает её в Command.
func Parse(hexStr string) (*Command, error) {
	raw, err := DecodeHex(hexStr)
	if err != nil {
		return nil, err
	}
	return ParseRaw(raw)
}

// DecodeHex декодирует HEX-строку в текст и снимает null-терминатор.
func DecodeHex(hexStr string) (string, error) {
	b, err := hex.DecodeString(strings.TrimSpace(hexStr))
	if err != nil {
		return "", fmt.Errorf("decode hex %q: %w", hexStr, err)
	}
	return strings.TrimRight(string(b), "\x00"), nil
}

// ParseRaw разбирает уже декодированную строку команды.
func ParseRaw(raw string) (*Command, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, "|")
	if len(parts) < 2 {
		return nil, fmt.Errorf("команда %q: ожидался формат host|action|channel", raw)
	}

	host := strings.TrimSpace(parts[0])
	if host == "" {
		return nil, fmt.Errorf("команда %q: пустой адрес устройства", raw)
	}
	ip, user, password, err := splitCredentials(host)
	if err != nil {
		return nil, fmt.Errorf("команда %q: %w", raw, err)
	}

	action, err := parseAction(parts[1])
	if err != nil {
		return nil, fmt.Errorf("команда %q: %w", raw, err)
	}

	channel := AllChannels
	if len(parts) > 2 {
		if s := strings.TrimSpace(parts[2]); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				return nil, fmt.Errorf("команда %q: канал %q не число", raw, s)
			}
			if n != 0 && n != 1 {
				return nil, fmt.Errorf("команда %q: канал %d, допустимы 0 и 1", raw, n)
			}
			channel = n
		}
	}

	// Части после канала — это цели. Их может не быть вовсе.
	var targetParts []string
	if len(parts) > 3 {
		targetParts = parts[3:]
	}
	targets, err := parseTargets(targetParts)
	if err != nil {
		return nil, fmt.Errorf("команда %q: %w", raw, err)
	}

	// Для команд управления канал обязателен: непонятно, чем именно щёлкать.
	if action != ActionStatus && channel == AllChannels {
		return nil, fmt.Errorf("команда %q: для действия %q нужен номер канала", raw, action)
	}
	// Безномерная канальная цель ("state") относится к каналу из команды,
	// поэтому без номера канала её не к чему привязать. Цели с номером
	// ("state0", "power1") самодостаточны.
	if channel == AllChannels && needsChannelNumber(targets) {
		return nil, fmt.Errorf("команда %q: цель без номера канала требует указать канал "+
			"(или пишите цели с номером: state0, power1)", raw)
	}

	return &Command{
		Host:     host,
		IP:       ip,
		User:     user,
		Password: password,
		Action:   action,
		Channel:  channel,
		Targets:  targets,
		Raw:      raw,
	}, nil
}

// splitCredentials отделяет "user:pass@" от адреса устройства.
func splitCredentials(host string) (ip, user, password string, err error) {
	at := strings.LastIndex(host, "@")
	if at < 0 {
		return host, "", "", nil
	}

	creds := host[:at]
	ip = strings.TrimSpace(host[at+1:])
	if ip == "" {
		return "", "", "", fmt.Errorf("в адресе %q нет самого адреса после учётных данных", host)
	}
	if strings.TrimSpace(creds) == "" {
		return "", "", "", fmt.Errorf("в адресе %q есть @, но нет учётных данных перед ним", host)
	}
	if i := strings.Index(creds, ":"); i >= 0 {
		return ip, creds[:i], creds[i+1:], nil
	}
	return ip, creds, "", nil
}

// parseTargets разбирает пары "имя=id:subid".
func parseTargets(parts []string) (map[string]Addr, error) {
	var targets map[string]Addr

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue // пустая позиция — цель не задана
		}

		name, value, found := strings.Cut(p, "=")
		if !found {
			return nil, fmt.Errorf("цель %q: ожидался формат имя=id:subid", p)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if !knownTarget(name) {
			return nil, fmt.Errorf("неизвестная цель %q: допустимы %s (можно с номером канала: state0, power1) "+
				"и %s", name, strings.Join(channelTargets, ", "), strings.Join(deviceTargets, ", "))
		}

		addr, err := parseAddr(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("цель %q: %w", name, err)
		}
		if targets == nil {
			targets = make(map[string]Addr, len(parts))
		}
		if _, dup := targets[name]; dup {
			return nil, fmt.Errorf("цель %q указана дважды", name)
		}
		targets[name] = addr
	}
	return targets, nil
}

// knownTarget проверяет имя цели, допуская номер канала в конце.
func knownTarget(name string) bool {
	for _, t := range deviceTargets {
		if name == t {
			return true
		}
	}
	for _, t := range channelTargets {
		if name == t || name == t+"0" || name == t+"1" {
			return true
		}
	}
	return false
}

// needsChannelNumber сообщает, что среди целей есть безномерная канальная —
// такой нужен явный номер канала в команде.
func needsChannelNumber(targets map[string]Addr) bool {
	for _, t := range channelTargets {
		if _, ok := targets[t]; ok {
			return true
		}
	}
	return false
}

// parseAddr разбирает адрес элемента умного дома "id:subid".
func parseAddr(s string) (Addr, error) {
	idStr, subStr, found := strings.Cut(s, ":")
	if !found {
		return Addr{}, fmt.Errorf("адрес %q: ожидался формат id:subid", s)
	}

	id, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 16)
	if err != nil {
		return Addr{}, fmt.Errorf("адрес %q: id должен быть числом 0..65535", s)
	}
	sub, err := strconv.ParseUint(strings.TrimSpace(subStr), 10, 8)
	if err != nil {
		return Addr{}, fmt.Errorf("адрес %q: subid должен быть числом 0..255", s)
	}
	return Addr{ID: uint16(id), SubID: uint8(sub)}, nil
}

// parseAction приводит написание действия к канону и принимает синонимы,
// которыми умный дом может оперировать (1/0/true/false).
func parseAction(s string) (Action, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "1", "true":
		return ActionOn, nil
	case "off", "0", "false":
		return ActionOff, nil
	case "toggle", "switch":
		return ActionToggle, nil
	case "status", "state", "get":
		return ActionStatus, nil
	default:
		return "", fmt.Errorf("неизвестное действие %q (on|off|toggle|status)", s)
	}
}
