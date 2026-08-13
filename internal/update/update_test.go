package update

import "testing"

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		name            string
		current, latest string
		want            bool
	}{
		{"патч выше", "v1.2.3", "v1.2.4", true},
		{"минор выше", "v1.2.9", "v1.3.0", true},
		{"мажор выше", "v1.9.9", "v2.0.0", true},
		{"та же версия", "v1.2.3", "v1.2.3", false},
		{"установлена новее", "v1.3.0", "v1.2.9", false},
		{"без префикса v", "1.2.3", "1.2.4", true},
		{"смешанный префикс", "1.2.3", "v1.2.4", true},
		// Десятки не должны сравниваться как строки: "1.10.0" > "1.9.0".
		{"двузначный минор", "v1.9.0", "v1.10.0", true},
		{"двузначный патч", "v1.2.9", "v1.2.10", true},
		// Сборка из git-описания номером не является: предлагать по ней
		// обновление нельзя, иначе разработочная сборка вечно устаревшая.
		{"сборка из git", "f24ced0-dirty", "v1.2.3", false},
		{"пусто", "", "v1.2.3", false},
		{"мусор в ответе", "v1.2.3", "последняя", false},
		{"предрелиз отбрасывается", "v1.2.3", "v1.2.3-rc1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Newer(tc.current, tc.latest); got != tc.want {
				t.Errorf("Newer(%q, %q) = %v, ожидалось %v", tc.current, tc.latest, got, tc.want)
			}
		})
	}
}

func TestInfoBeforeCheck(t *testing.T) {
	c := New("v1.0.0")
	info := c.Info()
	if info.Current != "v1.0.0" || info.Available {
		t.Errorf("до проверки: %+v", info)
	}
}
