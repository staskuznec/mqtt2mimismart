package shelly

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestServer поднимает фейковое реле и отдаёт клиент, нацеленный на него.
func newTestServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL), srv
}

func TestOnSendsTurnParam(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/relay/0" {
			t.Errorf("путь = %q, ожидался /relay/0", r.URL.Path)
		}
		if got := r.URL.Query().Get("turn"); got != "on" {
			t.Errorf("turn = %q, ожидался on", got)
		}
		w.Write([]byte(`{"ison":true,"has_timer":false,"source":"http"}`))
	})

	st, err := c.On(context.Background(), Channel0)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsOn {
		t.Errorf("IsOn = false, ожидался true")
	}
}

func TestOnWithTimer(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("turn") != "on" || q.Get("timer") != "60" {
			t.Errorf("query = %v, ожидались turn=on&timer=60", q)
		}
		w.Write([]byte(`{"ison":true,"has_timer":true,"timer_remaining":60}`))
	})

	st, err := c.OnWithTimer(context.Background(), Channel0, 60)
	if err != nil {
		t.Fatal(err)
	}
	if !st.HasTimer || st.TimerRemaining != 60 {
		t.Errorf("таймер разобран неверно: %+v", st)
	}
}

func TestStatusParsing(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"relays":[{"ison":true},{"ison":false}],
			"meters":[{"power":42.5,"is_valid":true},{"power":0}],
			"tmp":{"tC":38.2,"is_valid":true},
			"overtemperature":false
		}`))
	})

	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Relays) != 2 || !st.Relays[0].IsOn || st.Relays[1].IsOn {
		t.Errorf("реле разобраны неверно: %+v", st.Relays)
	}
	if st.Meters[0].Power != 42.5 {
		t.Errorf("мощность = %v, ожидалось 42.5", st.Meters[0].Power)
	}
	if st.Tmp.TC != 38.2 {
		t.Errorf("температура = %v, ожидалось 38.2", st.Tmp.TC)
	}
}

func TestInvalidChannel(t *testing.T) {
	c := New("127.0.0.1")
	if _, err := c.On(context.Background(), Channel(5)); err == nil {
		t.Error("ожидалась ошибка для канала 5")
	}
}

func TestBasicAuth(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "admin" || p != "secret" {
			t.Errorf("basic auth = %q/%q ok=%v", u, p, ok)
		}
		w.Write([]byte(`{"ison":false}`))
	})
	c.user, c.pass = "admin", "secret"

	if _, err := c.Off(context.Background(), Channel0); err != nil {
		t.Fatal(err)
	}
}

func TestAPIError(t *testing.T) {
	c, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Unauthorized"))
	})

	_, err := c.Status(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка при HTTP 401")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != http.StatusUnauthorized {
		t.Errorf("ожидался *APIError со статусом 401, получено %v", err)
	}
}
