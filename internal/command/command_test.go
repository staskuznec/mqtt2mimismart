package command

import (
	"encoding/hex"
	"testing"
)

func toHex(s string) string { return hex.EncodeToString([]byte(s)) }

func TestParseActions(t *testing.T) {
	tests := []struct {
		raw     string
		action  Action
		channel int
	}{
		{"192.168.1.50|on|0", ActionOn, 0},
		{"192.168.1.50|off|1", ActionOff, 1},
		{"192.168.1.50|toggle|0", ActionToggle, 0},
		{"192.168.1.50|status|1", ActionStatus, 1},
		{"192.168.1.50|status|", ActionStatus, AllChannels},
		{"192.168.1.50|status", ActionStatus, AllChannels},
		{"192.168.1.50|1|0", ActionOn, 0},  // синоним on
		{"192.168.1.50|0|1", ActionOff, 1}, // синоним off
		{"192.168.1.50|STATUS|0", ActionStatus, 0},
	}

	for _, tt := range tests {
		cmd, err := ParseRaw(tt.raw)
		if err != nil {
			t.Errorf("ParseRaw(%q) вернул ошибку: %v", tt.raw, err)
			continue
		}
		if cmd.Action != tt.action || cmd.Channel != tt.channel {
			t.Errorf("ParseRaw(%q) = action %q channel %d, ожидались %q и %d",
				tt.raw, cmd.Action, cmd.Channel, tt.action, tt.channel)
		}
		if cmd.IP != "192.168.1.50" {
			t.Errorf("ParseRaw(%q): ip = %q", tt.raw, cmd.IP)
		}
	}
}

func TestParseHexWithNullTerminator(t *testing.T) {
	cmd, err := Parse(toHex("192.168.1.50|on|0\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.IP != "192.168.1.50" || cmd.Action != ActionOn || cmd.Channel != 0 {
		t.Errorf("разобрано неверно: %+v", cmd)
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		"192.168.1.50",        // нет действия
		"|on|0",               // пустой ip
		"192.168.1.50|open|0", // неизвестное действие
		"192.168.1.50|on|",    // управление без канала
		"192.168.1.50|on|5",   // канала 5 не существует
		"192.168.1.50|on|abc", // канал не число
	}
	for _, raw := range bad {
		if cmd, err := ParseRaw(raw); err == nil {
			t.Errorf("ParseRaw(%q) ошибки не вернул, получено %+v", raw, cmd)
		}
	}
}

func TestDecodeHexInvalid(t *testing.T) {
	if _, err := Parse("zzzz"); err == nil {
		t.Error("ожидалась ошибка на некорректном HEX")
	}
}

// TestFromArgsFindsCommandAnywhere — команда должна находиться независимо
// от того, в каком по счёту аргументе её передал умный дом.
func TestFromArgsFindsCommandAnywhere(t *testing.T) {
	payload := toHex("192.168.20.222|on|0")

	tests := []struct {
		name    string
		args    []string
		wantIdx int
	}{
		{"второй аргумент", []string{"./shelly25", "x", payload}, 2},
		{"третий аргумент", []string{"./shelly25", "x", "y", payload}, 3},
		{"первый аргумент", []string{"./shelly25", payload}, 1},
		{"среди мусора", []string{"./shelly25", "12", "abcdef", payload, "tail"}, 3},
	}

	for _, tt := range tests {
		cmd, idx, err := FromArgs(tt.args)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if idx != tt.wantIdx {
			t.Errorf("%s: индекс = %d, ожидался %d", tt.name, idx, tt.wantIdx)
		}
		if cmd.IP != "192.168.20.222" || cmd.Action != ActionOn || cmd.Channel != 0 {
			t.Errorf("%s: разобрано неверно: %+v", tt.name, cmd)
		}
	}
}

func TestFromArgsNoCommand(t *testing.T) {
	if _, _, err := FromArgs([]string{"./shelly25", "x", "y"}); err == nil {
		t.Error("ожидалась ошибка, когда команды нет ни в одном аргументе")
	}
	if _, _, err := FromArgs([]string{"./shelly25"}); err == nil {
		t.Error("ожидалась ошибка при отсутствии аргументов")
	}
}

func TestFromArgsPinnedIndex(t *testing.T) {
	payload := toHex("192.168.20.222|off|1")
	t.Setenv(EnvArgIndex, "2")

	cmd, idx, err := FromArgs([]string{"./shelly25", "x", payload})
	if err != nil {
		t.Fatal(err)
	}
	if idx != 2 || cmd.Action != ActionOff || cmd.Channel != 1 {
		t.Errorf("idx=%d cmd=%+v", idx, cmd)
	}

	// Закреплённый индекс указывает на несуществующий аргумент.
	if _, _, err := FromArgs([]string{"./shelly25"}); err == nil {
		t.Error("ожидалась ошибка при закреплённом индексе вне диапазона")
	}
}

// TestParseTargets — адреса назначения приезжают прямо в команде.
func TestParseTargets(t *testing.T) {
	cmd, err := ParseRaw("192.168.20.222|status|0|state=563:111|power=563:113|energy=563:114|temp=563:115")
	if err != nil {
		t.Fatal(err)
	}
	if !cmd.HasTargets() {
		t.Fatal("цели не разобраны")
	}

	want := map[string]Addr{
		TargetState:  {ID: 563, SubID: 111},
		TargetPower:  {ID: 563, SubID: 113},
		TargetEnergy: {ID: 563, SubID: 114},
		TargetTemp:   {ID: 563, SubID: 115},
	}
	for name, w := range want {
		got, ok := cmd.Target(name)
		if !ok {
			t.Errorf("цель %q не найдена", name)
			continue
		}
		if got != w {
			t.Errorf("цель %q = %v, ожидалась %v", name, got, w)
		}
	}
}

// TestParseTargetsSkipsEmpty — пустые позиции просто пропускаются.
func TestParseTargetsSkipsEmpty(t *testing.T) {
	cmd, err := ParseRaw("192.168.20.222|status|1||state=563:121||temp=563:125|")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Targets) != 2 {
		t.Errorf("целей = %d, ожидалось 2: %v", len(cmd.Targets), cmd.Targets)
	}
	if cmd.Channel != 1 {
		t.Errorf("канал = %d, ожидался 1", cmd.Channel)
	}
}

func TestParseCredentials(t *testing.T) {
	cmd, err := ParseRaw("shadow:misteR48@192.168.20.222|on|0")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.IP != "192.168.20.222" {
		t.Errorf("ip = %q", cmd.IP)
	}
	if cmd.User != "shadow" || cmd.Password != "misteR48" {
		t.Errorf("учётные данные = %q/%q", cmd.User, cmd.Password)
	}

	// Пароль может содержать символ @ — режем по последнему.
	cmd, err = ParseRaw("user:pa@ss@10.0.0.5|off|1")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.IP != "10.0.0.5" || cmd.Password != "pa@ss" {
		t.Errorf("ip = %q, пароль = %q", cmd.IP, cmd.Password)
	}

	// Без учётных данных поля пустые.
	cmd, err = ParseRaw("192.168.20.222|on|0")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.User != "" || cmd.Password != "" {
		t.Errorf("неожиданные учётные данные %q/%q", cmd.User, cmd.Password)
	}
}

func TestParseTargetErrors(t *testing.T) {
	bad := []string{
		"192.168.20.222|status|0|state",               // нет знака =
		"192.168.20.222|status|0|wat=563:111",         // неизвестная цель
		"192.168.20.222|status|0|state=563",           // адрес без subid
		"192.168.20.222|status|0|state=abc:111",       // id не число
		"192.168.20.222|status|0|state=563:999",       // subid больше 255
		"192.168.20.222|status|0|state=99999:1",       // id больше 65535
		"192.168.20.222|status|0|state=1:1|state=2:2", // дубль цели
		"192.168.20.222|status||state=563:111",        // цели без номера канала
		"@192.168.20.222|on|0",                        // пустой адрес после логина
	}
	for _, raw := range bad {
		if cmd, err := ParseRaw(raw); err == nil {
			t.Errorf("ParseRaw(%q) ошибки не вернул, получено %+v", raw, cmd)
		}
	}
}

func TestIsAllChannels(t *testing.T) {
	cmd, err := ParseRaw("192.168.1.50|status|")
	if err != nil {
		t.Fatal(err)
	}
	if !cmd.IsAllChannels() {
		t.Error("ожидалось IsAllChannels() == true")
	}
}
