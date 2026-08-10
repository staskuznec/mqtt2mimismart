package shelly

import "context"

// Info — ответ эндпоинта /shelly (единственный, не требующий авторизации).
type Info struct {
	Type       string `json:"type"` // модель, для Shelly 2.5 — "SHSW-25"
	MAC        string `json:"mac"`  // MAC-адрес
	Auth       bool   `json:"auth"` // включена ли Basic Auth
	FW         string `json:"fw"`   // версия прошивки
	Discoverab bool   `json:"discoverable"`
	NumOutputs int    `json:"num_outputs"` // число реле (2 у Shelly 2.5)
	NumMeters  int    `json:"num_meters"`
}

// Info возвращает идентификацию устройства (/shelly).
// Удобно для проверки доступности и того, что это действительно SHSW-25.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	var out Info
	if err := c.get(ctx, "/shelly", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
