package shs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	shclient "github.com/staskuznec/shClientMimismart"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEventAddr(t *testing.T) {
	e := Event{ID: 563, SubID: 57}
	if got := e.Addr(); got != "563:57" {
		t.Errorf("Addr() = %q, ожидалось %q", got, "563:57")
	}
}

// До подключения отправлять некуда, и об этом надо говорить вызывающему:
// молча проглоченная команда выглядит как неисправное реле.
func TestSendWithoutConnection(t *testing.T) {
	c := New(Config{Addr: "127.0.0.1:9001", Key: "0123456789abcdef"}, testLogger())

	value := shclient.Byte(563, 57, 1)
	err := c.Send(context.Background(), value)
	if !errors.Is(err, shclient.ErrNotConnected) {
		t.Errorf("Send без соединения = %v, ожидалась ErrNotConnected", err)
	}
}

func TestInitialStatus(t *testing.T) {
	c := New(Config{Addr: "127.0.0.1:9001", Key: "0123456789abcdef"}, testLogger())

	st := c.Status()
	if st.Phase != PhaseDisconnected {
		t.Errorf("начальная фаза = %q, ожидалась %q", st.Phase, PhaseDisconnected)
	}
	if st.Events != 0 || st.Sent != 0 || st.Connects != 0 {
		t.Errorf("счётчики не нулевые: %+v", st)
	}
	if len(c.Logic()) != 0 {
		t.Error("logic.xml не пуст до подключения")
	}
}

// Отмена контекста — штатное завершение: Run обязан вернуть управление, а не
// уйти в бесконечное переподключение к недоступному серверу.
func TestRunStopsOnContextCancel(t *testing.T) {
	// Порт заведомо занят никем: подключение упадёт сразу, и мы проверим,
	// что супервизор не игнорирует отмену между попытками.
	c := New(Config{Addr: "127.0.0.1:1", Key: "0123456789abcdef"}, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run вернул %v, ожидалась context.Canceled", err)
	}
}

// Неверная длина ключа должна ронять попытку подключения, а не уходить в
// молчаливый цикл переподключений.
func TestDialRejectsBadKey(t *testing.T) {
	c := New(Config{Addr: "127.0.0.1:9001", Key: "short"}, testLogger())

	_, _, err := c.dial(context.Background())
	if !errors.Is(err, shclient.ErrInvalidKey) {
		t.Errorf("dial с коротким ключом = %v, ожидалась ErrInvalidKey", err)
	}
}
