// Package shelly — минимальный клиент для реле Shelly 2.5 (Gen1 HTTP REST API).
//
// Устройство работает в режиме relay (два независимых канала O1/O2).
// Физические выключатели в Shelly не заведены — управление идёт только по REST
// со стороны сервера умного дома. Привязка выключателей к реле — забота умного
// дома, здесь её нет намеренно.
//
// Справка по API: https://shelly-api-docs.shelly.cloud/gen1/#shelly2-5
package shelly

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Channel — номер релейного канала. Shelly 2.5 имеет два: 0 (O1) и 1 (O2).
type Channel int

const (
	Channel0 Channel = 0 // выход O1
	Channel1 Channel = 1 // выход O2
)

// Valid сообщает, что канал существует на Shelly 2.5.
func (c Channel) Valid() bool { return c == Channel0 || c == Channel1 }

// Client — потокобезопасный клиент одного устройства Shelly 2.5.
// Создаётся через New и переиспользуется (внутри — общий http.Client).
type Client struct {
	baseURL string
	httpc   *http.Client
	user    string
	pass    string
}

// Option настраивает Client в New.
type Option func(*Client)

// WithBasicAuth задаёт логин/пароль, если на реле включена защита
// (Settings → Restrict login). Без защиты вызывать не нужно.
func WithBasicAuth(user, pass string) Option {
	return func(c *Client) { c.user, c.pass = user, pass }
}

// WithHTTPClient подставляет свой *http.Client (таймауты, транспорт, пул).
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.httpc = h
		}
	}
}

// WithTimeout задаёт таймаут запроса, если не передан свой http.Client.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.httpc.Timeout = d }
}

// New создаёт клиент для устройства по его адресу.
// host — это IP или hostname реле, допускается со схемой и без:
// "192.168.1.50", "http://192.168.1.50", "shelly-25.local".
// Если адрес содержит логин и пароль ("http://user:pass@192.168.1.50"), они
// вынимаются из URL в заголовок Authorization: иначе учётные данные попали бы
// в текст сетевых ошибок и в логи. Явный [WithBasicAuth] имеет приоритет.
func New(host string, opts ...Option) *Client {
	host = strings.TrimSpace(host)
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}
	host = strings.TrimRight(host, "/")

	c := &Client{
		httpc: &http.Client{Timeout: 5 * time.Second},
	}

	if u, err := url.Parse(host); err == nil && u.User != nil {
		c.user = u.User.Username()
		c.pass, _ = u.User.Password()
		u.User = nil
		host = strings.TrimRight(u.String(), "/")
	}
	c.baseURL = host

	for _, o := range opts {
		o(c)
	}
	return c
}

// APIError — ненулевой HTTP-статус от устройства.
type APIError struct {
	Status int
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("shelly: %s -> HTTP %d: %s", e.Path, e.Status, e.Body)
}

// get выполняет GET path с query-параметрами и разбирает JSON-ответ в out.
// out может быть nil, если тело ответа не нужно.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("shelly: build request: %w", err)
	}
	if c.user != "" || c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("shelly: request %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Path: path, Body: strings.TrimSpace(string(body))}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("shelly: decode %s: %w", path, err)
	}
	return nil
}

// itoa — короткий помощник для query-параметров.
func itoa(i int) string { return strconv.Itoa(i) }
