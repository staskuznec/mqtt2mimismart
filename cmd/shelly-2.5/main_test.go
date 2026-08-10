package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"Shelly_2.5/internal/command"
	"Shelly_2.5/internal/config"
	"Shelly_2.5/internal/reporter"
	"Shelly_2.5/internal/shelly"
)

// statusJSON — реалистичный ответ /status от Shelly 2.5 в режиме relay:
// канал 0 включён и тянет 41.2 Вт, канал 1 выключен.
const statusJSON = `{
  "relays": [
    {"ison": true,  "has_timer": false, "overpower": false, "is_valid": true, "source": "http"},
    {"ison": false, "has_timer": false, "overpower": false, "is_valid": true, "source": "input"}
  ],
  "meters": [
    {"power": 41.2, "overpower": 0, "is_valid": true, "total": 1234},
    {"power": 0,    "overpower": 0, "is_valid": true, "total": 0}
  ],
  "temperature": 42.3,
  "overtemperature": false,
  "tmp": {"tC": 42.3, "tF": 108.14, "is_valid": true},
  "uptime": 98765
}`

// fakeRelay поднимает HTTP-сервер, отвечающий как реле Shelly 2.5.
func fakeRelay(t *testing.T) (*shelly.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/status":
			_, _ = w.Write([]byte(statusJSON))
		case strings.HasPrefix(r.URL.Path, "/relay/"):
			turn := r.URL.Query().Get("turn")
			_, _ = w.Write([]byte(`{"ison":` + map[string]string{
				"on": "true", "off": "false", "toggle": "true",
			}[turn] + `,"has_timer":false,"source":"http"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return shelly.New(srv.URL), srv
}

// testDevice — устройство с двумя каналами, мощностью и температурой.
func testDevice(ip string) *config.Device {
	return &config.Device{
		IP:   ip,
		Name: "Тестовое реле",
		Channels: []config.Channel{
			{
				Channel: 0,
				Name:    "Канал 0",
				State:   config.Point{ID: 100, SubID: 0},
				Power:   &config.Point{ID: 100, SubID: 1, Scale: 0.1},
			},
			{
				Channel: 1,
				Name:    "Канал 1",
				State:   config.Point{ID: 101, SubID: 0},
			},
		},
		Temperature: &config.Point{ID: 100, SubID: 9},
	}
}

func TestCollectAllChannels(t *testing.T) {
	client, srv := fakeRelay(t)
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	cmd, err := command.ParseRaw(srv.URL + "|status|")
	if err != nil {
		t.Fatal(err)
	}
	readings, err := collect(testDevice(srv.URL), cmd, status)
	if err != nil {
		t.Fatal(err)
	}

	// канал 0: состояние + мощность, канал 1: состояние, плюс температура
	if len(readings) != 4 {
		t.Fatalf("значений = %d, ожидалось 4: %+v", len(readings), readings)
	}

	want := []struct {
		id    uint16
		subID uint8
		value float64
	}{
		{100, 0, 1},    // канал 0 включён
		{100, 1, 41.2}, // мощность канала 0 (масштаб применяется при отправке)
		{101, 0, 0},    // канал 1 выключен
		{100, 9, 42.3}, // температура устройства
	}
	for i, w := range want {
		got := readings[i]
		if got.Point.ID != w.id || got.Point.SubID != w.subID || got.Value != w.value {
			t.Errorf("значение %d: id=%d subid=%d value=%v, ожидалось id=%d subid=%d value=%v",
				i, got.Point.ID, got.Point.SubID, got.Value, w.id, w.subID, w.value)
		}
	}
}

func TestCollectSingleChannel(t *testing.T) {
	client, srv := fakeRelay(t)
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	cmd, err := command.ParseRaw(srv.URL + "|status|1")
	if err != nil {
		t.Fatal(err)
	}
	readings, err := collect(testDevice(srv.URL), cmd, status)
	if err != nil {
		t.Fatal(err)
	}

	// только канал 1 (у него нет мощности) плюс температура устройства
	if len(readings) != 2 {
		t.Fatalf("значений = %d, ожидалось 2: %+v", len(readings), readings)
	}
	if readings[0].Point.ID != 101 || readings[0].Value != 0 {
		t.Errorf("канал 1 разобран неверно: %+v", readings[0])
	}
}

func TestCollectUnknownChannel(t *testing.T) {
	client, srv := fakeRelay(t)
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := command.ParseRaw(srv.URL + "|status|0")
	if err != nil {
		t.Fatal(err)
	}

	// В конфиге описан только канал 1 — запрошенного канала 0 там нет.
	device := &config.Device{
		IP: srv.URL,
		Channels: []config.Channel{
			{Channel: 1, State: config.Point{ID: 101}},
		},
	}
	if _, err := collect(device, cmd, status); err == nil {
		t.Error("ожидалась ошибка для канала, которого нет в конфиге")
	}
}

func TestApplyActions(t *testing.T) {
	client, _ := fakeRelay(t)
	tests := []struct {
		action command.Action
		wantOn bool
	}{
		{command.ActionOn, true},
		{command.ActionOff, false},
		{command.ActionToggle, true},
	}
	for _, tt := range tests {
		state, err := apply(context.Background(), client,
			&command.Command{Action: tt.action, Channel: 0})
		if err != nil {
			t.Errorf("%s: %v", tt.action, err)
			continue
		}
		if state.IsOn != tt.wantOn {
			t.Errorf("%s: ison = %v, ожидалось %v", tt.action, state.IsOn, tt.wantOn)
		}
	}
}

// TestLabels проверяет, что в логах и сухом прогоне видно понятные имена.
func TestLabels(t *testing.T) {
	device := testDevice("192.168.20.222")
	if got := deviceLabel(device); got != "Тестовое реле" {
		t.Errorf("deviceLabel = %q", got)
	}
	if got := channelLabel(device, &device.Channels[0]); got != "Канал 0" {
		t.Errorf("channelLabel = %q", got)
	}

	unnamed := &config.Device{IP: "10.0.0.1", Channels: []config.Channel{{Channel: 1}}}
	if got := channelLabel(unnamed, &unnamed.Channels[0]); got != "10.0.0.1 ch1" {
		t.Errorf("channelLabel без имени = %q", got)
	}
}

// Проверяем, что Reading из collect можно напечатать в сухом прогоне.
var _ = reporter.Reading{}

// TestCollectTargetsFromScriptCommand прогоняет ровно ту строку, которую
// скрипт MimiSmart собирает по таймеру, и проверяет, что состояния ОБОИХ
// реле в неё попадают и раскладываются по своим адресам.
func TestCollectTargetsFromScriptCommand(t *testing.T) {
	client, srv := fakeRelay(t)
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	raw := srv.URL + "|status||state0=563:250|power0=563:249|energy0=563:248|" +
		"state1=563:247|power1=563:246|energy1=563:245|voltage=563:244|temp=563:243"

	cmd, err := command.ParseRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !cmd.IsAllChannels() {
		t.Fatalf("канал = %d, ожидался AllChannels", cmd.Channel)
	}
	if got := cmd.TargetChannels(); len(got) != 2 {
		t.Fatalf("каналов с целями = %v, ожидались оба", got)
	}

	readings, err := collectTargets(cmd, status)
	if err != nil {
		t.Fatal(err)
	}

	got := make(map[string]float64, len(readings))
	for _, r := range readings {
		got[fmt.Sprintf("%d:%d", r.Point.ID, r.Point.SubID)] = r.Value
	}

	// В fakeRelay канал 0 включён и тянет 41.2 Вт, канал 1 выключен.
	want := map[string]float64{
		"563:250": 1,    // состояние реле 0
		"563:249": 41.2, // мощность реле 0
		"563:247": 0,    // состояние реле 1
		"563:246": 0,    // мощность реле 1
		"563:243": 42.3, // температура
	}
	for addr, w := range want {
		v, ok := got[addr]
		if !ok {
			t.Errorf("значение для %s не отправлено вовсе", addr)
			continue
		}
		if v != w {
			t.Errorf("значение для %s = %v, ожидалось %v", addr, v, w)
		}
	}

	if len(readings) != 8 {
		t.Errorf("значений = %d, ожидалось 8: %v", len(readings), got)
	}
}
