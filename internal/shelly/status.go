package shelly

import "context"

// Meter — показания измерителя мощности по каналу (элемент meters в /status).
type Meter struct {
	Power     float64   `json:"power"`     // текущая мощность, Вт
	Overpower float64   `json:"overpower"` // порог защиты, Вт
	IsValid   bool      `json:"is_valid"`
	Timestamp int64     `json:"timestamp"`
	Counters  []float64 `json:"counters"` // мощность за последние минуты
	Total     float64   `json:"total"`    // накопленная энергия, Вт·мин
}

// Temperature — блок tmp в /status (датчик температуры устройства).
type Temperature struct {
	TC      float64 `json:"tC"` // °C
	TF      float64 `json:"tF"` // °F
	IsValid bool    `json:"is_valid"`
}

// Status — срез состояния устройства (/status), только нужные для реле поля.
type Status struct {
	Relays []RelayState `json:"relays"` // индекс = номер канала
	Meters []Meter      `json:"meters"`

	// Voltage — напряжение сети. Оно одно на всё устройство: оба выхода
	// питаются от одной и той же фазы L.
	Voltage float64 `json:"voltage"`

	Temperature     float64     `json:"temperature"`     // °C, устаревшее плоское поле
	OverTemperature bool        `json:"overtemperature"` // перегрев
	Tmp             Temperature `json:"tmp"`
	Uptime          int64       `json:"uptime"`
	HasUpdate       bool        `json:"has_update"`
}

// Status возвращает полное состояние устройства (/status): оба реле,
// измерители мощности, температуру.
func (c *Client) Status(ctx context.Context) (*Status, error) {
	var out Status
	if err := c.get(ctx, "/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
