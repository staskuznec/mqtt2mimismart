package shelly

import (
	"context"
	"fmt"
	"net/url"
)

// RelayState — состояние одного релейного канала (/relay/{index}).
type RelayState struct {
	IsOn           bool   `json:"ison"`      // включено ли реле
	HasTimer       bool   `json:"has_timer"` // активен ли авто-таймер
	TimerStarted   int64  `json:"timer_started_at"`
	TimerDuration  int    `json:"timer_duration"`
	TimerRemaining int    `json:"timer_remaining"` // сек до авто-переключения
	Overpower      bool   `json:"overpower"`       // сработала защита по мощности
	IsValid        bool   `json:"is_valid"`
	Source         string `json:"source"` // источник последней команды
}

// errBadChannel — единый текст ошибки для неверного канала.
func errBadChannel(ch Channel) error {
	return fmt.Errorf("shelly: неверный канал %d (допустимы 0 и 1)", int(ch))
}

// Relay возвращает текущее состояние канала (GET /relay/{index}).
func (c *Client) Relay(ctx context.Context, ch Channel) (*RelayState, error) {
	if !ch.Valid() {
		return nil, errBadChannel(ch)
	}
	var out RelayState
	if err := c.get(ctx, "/relay/"+itoa(int(ch)), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// setRelay — общий вызов /relay/{index} с параметром turn (+ опц. timer).
func (c *Client) setRelay(ctx context.Context, ch Channel, turn string, timer int) (*RelayState, error) {
	if !ch.Valid() {
		return nil, errBadChannel(ch)
	}
	q := url.Values{}
	q.Set("turn", turn)
	if timer > 0 {
		q.Set("timer", itoa(timer))
	}
	var out RelayState
	if err := c.get(ctx, "/relay/"+itoa(int(ch)), q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// On включает канал. Возвращает новое состояние.
func (c *Client) On(ctx context.Context, ch Channel) (*RelayState, error) {
	return c.setRelay(ctx, ch, "on", 0)
}

// Off выключает канал.
func (c *Client) Off(ctx context.Context, ch Channel) (*RelayState, error) {
	return c.setRelay(ctx, ch, "off", 0)
}

// Toggle инвертирует состояние канала.
func (c *Client) Toggle(ctx context.Context, ch Channel) (*RelayState, error) {
	return c.setRelay(ctx, ch, "toggle", 0)
}

// Set включает (on=true) или выключает (on=false) канал — удобно, когда
// умный дом хранит желаемое булево состояние.
func (c *Client) Set(ctx context.Context, ch Channel, on bool) (*RelayState, error) {
	if on {
		return c.On(ctx, ch)
	}
	return c.Off(ctx, ch)
}

// OnWithTimer включает канал и через timerSec секунд автоматически выключает
// его (аппаратный таймер самого реле — работает даже если сервер отвалится).
func (c *Client) OnWithTimer(ctx context.Context, ch Channel, timerSec int) (*RelayState, error) {
	return c.setRelay(ctx, ch, "on", timerSec)
}

// OffWithTimer выключает канал и через timerSec секунд снова включает.
func (c *Client) OffWithTimer(ctx context.Context, ch Channel, timerSec int) (*RelayState, error) {
	return c.setRelay(ctx, ch, "off", timerSec)
}
