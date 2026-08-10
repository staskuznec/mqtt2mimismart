package shelly

import (
	"context"
	"net/url"
)

// Тип входного контакта (btn_type) в /settings/relay/{index}.
// Даже без подключённых к Shelly выключателей значение detached гарантирует,
// что случайный потенциал на входе не переключит нагрузку: реле слушает
// только REST.
const (
	BtnMomentary        = "momentary"
	BtnToggle           = "toggle"
	BtnEdge             = "edge"
	BtnDetached         = "detached" // вход не управляет своим реле
	BtnMomentaryOnRelse = "momentary_on_release"
)

// Поведение канала при подаче питания (default_state).
const (
	DefaultOff  = "off"
	DefaultOn   = "on"
	DefaultLast = "last" // восстановить состояние до отключения
)

// RelaySettings — конфигурация канала (/settings/relay/{index}).
type RelaySettings struct {
	Name         string  `json:"name"`
	ApplianceT   string  `json:"appliance_type"`
	IsOn         bool    `json:"ison"`
	DefaultState string  `json:"default_state"`
	BtnType      string  `json:"btn_type"`
	BtnReverse   int     `json:"btn_reverse"`
	AutoOn       float64 `json:"auto_on"`  // авто-включение через N сек (0 = выкл)
	AutoOff      float64 `json:"auto_off"` // авто-выключение через N сек (0 = выкл)
}

// RelaySettings читает настройки канала (GET /settings/relay/{index}).
func (c *Client) RelaySettings(ctx context.Context, ch Channel) (*RelaySettings, error) {
	if !ch.Valid() {
		return nil, errBadChannel(ch)
	}
	var out RelaySettings
	if err := c.get(ctx, "/settings/relay/"+itoa(int(ch)), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetName задаёт человекочитаемое имя канала.
func (c *Client) SetName(ctx context.Context, ch Channel, name string) (*RelaySettings, error) {
	return c.updateRelaySettings(ctx, ch, url.Values{"name": {name}})
}

// SetDefaultState задаёт поведение канала при включении питания
// (DefaultOff / DefaultOn / DefaultLast).
func (c *Client) SetDefaultState(ctx context.Context, ch Channel, state string) (*RelaySettings, error) {
	return c.updateRelaySettings(ctx, ch, url.Values{"default_state": {state}})
}

// SetButtonType меняет режим входа (см. константы Btn*). Для сценария
// «выключатели не в Shelly» имеет смысл выставить BtnDetached.
func (c *Client) SetButtonType(ctx context.Context, ch Channel, btnType string) (*RelaySettings, error) {
	return c.updateRelaySettings(ctx, ch, url.Values{"btn_type": {btnType}})
}

// updateRelaySettings — общий помощник записи /settings/relay/{index}.
func (c *Client) updateRelaySettings(ctx context.Context, ch Channel, q url.Values) (*RelaySettings, error) {
	if !ch.Valid() {
		return nil, errBadChannel(ch)
	}
	var out RelaySettings
	if err := c.get(ctx, "/settings/relay/"+itoa(int(ch)), q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
