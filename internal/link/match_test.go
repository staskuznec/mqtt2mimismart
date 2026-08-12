package link

import (
	"reflect"
	"testing"
)

func TestMatches(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter string
		topic  string
		want   bool
	}{
		{"точное совпадение", "shellies/shelly25-A1/relay/0", "shellies/shelly25-A1/relay/0", true},
		{"другой топик", "shellies/shelly25-A1/relay/0", "shellies/shelly25-A1/relay/1", false},

		{"плюс в середине", "shellies/+/relay/0", "shellies/shelly25-A1/relay/0", true},
		{"плюс в конце", "shellies/shelly25-A1/relay/+", "shellies/shelly25-A1/relay/1", true},
		{"два плюса", "shellies/+/relay/+", "shellies/shelly25-A1/relay/0", true},
		// "+" заменяет ровно один уровень, а не любое их число.
		{"плюс не берёт два уровня", "shellies/+", "shellies/shelly25-A1/relay/0", false},
		{"плюс требует уровень", "shellies/+/relay", "shellies/relay", false},

		{"решётка забирает хвост", "shellies/#", "shellies/shelly25-A1/relay/0", true},
		{"решётка на всё", "#", "shellies/shelly25-A1/relay/0", true},
		// По стандарту "#" совпадает и с родительским уровнем.
		{"решётка после точного", "shellies/shelly25-A1/#", "shellies/shelly25-A1", true},

		{"фильтр длиннее топика", "shellies/a/b/c", "shellies/a", false},
		{"топик длиннее фильтра", "shellies/a", "shellies/a/b", false},

		{"пустой фильтр", "", "shellies/a", false},
		{"пустой топик", "shellies/#", "", false},

		// Служебные топики брокера не должны попадать в снифер по "#":
		// иначе статистика $SYS забьёт его целиком.
		{"решётка не берёт служебные", "#", "$SYS/broker/uptime", false},
		{"плюс не берёт служебные", "+/broker/uptime", "$SYS/broker/uptime", false},
		{"явная подписка на служебные работает", "$SYS/#", "$SYS/broker/uptime", true},

		// "#" не последним — ошибка в фильтре, совпадением её считать нельзя.
		{"решётка не последняя", "shellies/#/relay", "shellies/a/relay", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(tc.filter, tc.topic); got != tc.want {
				t.Errorf("Matches(%q, %q) = %v, ожидалось %v", tc.filter, tc.topic, got, tc.want)
			}
		})
	}
}

func TestValidFilter(t *testing.T) {
	for _, tc := range []struct {
		filter string
		want   bool
	}{
		{"shellies/#", true},
		{"shellies/+/relay/0", true},
		{"#", true},
		{"+", true},
		{"", false},
		{"shellies/#/relay", false},
		{"shellies/rel#ay", false},
		{"shellies/rel+ay", false},
	} {
		t.Run(tc.filter, func(t *testing.T) {
			if got := ValidFilter(tc.filter); got != tc.want {
				t.Errorf("ValidFilter(%q) = %v, ожидалось %v", tc.filter, got, tc.want)
			}
		})
	}
}

// Публикуют всегда в конкретный топик: подстановочный знак в адресе команды
// означает, что связка настроена неверно и щёлкнет не тем реле.
func TestValidTopic(t *testing.T) {
	for _, tc := range []struct {
		topic string
		want  bool
	}{
		{"shellies/shelly25-A1/relay/0/command", true},
		{"shellies/+/relay/0/command", false},
		{"shellies/#", false},
		{"", false},
	} {
		t.Run(tc.topic, func(t *testing.T) {
			if got := ValidTopic(tc.topic); got != tc.want {
				t.Errorf("ValidTopic(%q) = %v, ожидалось %v", tc.topic, got, tc.want)
			}
		})
	}
}

func TestCaptures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter string
		topic  string
		want   []string
	}{
		{
			name:   "устройство и канал",
			filter: "shellies/+/relay/+",
			topic:  "shellies/shelly25-A1B2C3/relay/0",
			want:   []string{"shelly25-A1B2C3", "0"},
		},
		{
			name:   "хвост по решётке",
			filter: "shellies/shelly25-A1/#",
			topic:  "shellies/shelly25-A1/relay/0",
			want:   []string{"relay/0"},
		},
		{
			name:   "без подстановок",
			filter: "shellies/shelly25-A1/relay/0",
			topic:  "shellies/shelly25-A1/relay/0",
			want:   nil,
		},
		{
			name:   "топик не подходит",
			filter: "shellies/+/relay/0",
			topic:  "other/thing",
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Captures(tc.filter, tc.topic)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Captures(%q, %q) = %v, ожидалось %v", tc.filter, tc.topic, got, tc.want)
			}
		})
	}
}
