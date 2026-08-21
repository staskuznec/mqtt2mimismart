package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/staskuznec/mqtt2mimismart/internal/link"
	"github.com/staskuznec/mqtt2mimismart/internal/store"
)

// Связка бывает подписана фильтром с подстановкой, и сравнение строк её не
// поймает: "shellies/+/relay/0" ловит топик, которого в самом фильтре не
// видно. А мастер-топик обязан резаться по границе уровня — иначе уборка
// одного устройства утаскивает соседнее с похожим именем.
func TestTopicCovered(t *testing.T) {
	for _, tc := range []struct {
		pattern   string
		tree      bool
		linkTopic string
		want      bool
	}{
		{"shellies/shelly1/relay/0", false, "shellies/shelly1/relay/0", true},
		{"shellies/shelly1/relay/0", false, "shellies/+/relay/0", true},
		{"shellies/shelly1/relay/0", false, "shellies/shelly1/#", true},
		{"shellies/shelly1/relay/0", false, "shellies/shelly1/relay/1", false},
		{"shellies/shelly1", true, "shellies/shelly1/relay/0", true},
		{"shellies/shelly1", true, "shellies/shelly1", true},
		{"shellies/shelly1", true, "shellies/shelly1pm-a8/relay/0", false},
	} {
		t.Run(tc.pattern+"|"+tc.linkTopic, func(t *testing.T) {
			if got := topicCovered(tc.pattern, tc.tree, tc.linkTopic); got != tc.want {
				t.Errorf("topicCovered = %v, ожидалось %v", got, tc.want)
			}
		})
	}
}

// Топик, с которого работает связка, убирают только по ошибке: на странице он
// выглядит так же, как чужой, а последствия разные — связка останется без
// источника и молча перестанет доносить значения.
func TestHideRefusesTopicUnderLink(t *testing.T) {
	db, h := testHandler(t)
	ctx := context.Background()

	if _, err := db.CreateLink(ctx, link.Link{
		Name: "Канал 0 — состояние", Enabled: true, Direction: link.In,
		Topic: "shellies/shelly1/relay/0", Encode: link.EncodeByte,
		TargetID: 563, TargetSubID: 1,
	}); err != nil {
		t.Fatalf("CreateLink: %v", err)
	}

	rec := post(h, "/topics/hide", "topic=shellies/shelly1&tree=1")

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("код ответа %d, ожидался %d", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Errorf("ответ увёл на %q — про отказ не сказано", loc)
	}
	hidden, err := db.HiddenTopics(ctx)
	if err != nil {
		t.Fatalf("HiddenTopics: %v", err)
	}
	if len(hidden) != 0 {
		t.Errorf("топик под связкой всё-таки скрыт: %v", hidden)
	}
}

// Чужой топик скрывается и остаётся скрытым: список лежит в базе, потому что
// снифер живёт в памяти и после перезапуска пуст.
func TestHideRemembersTopic(t *testing.T) {
	db, h := testHandler(t)

	rec := post(h, "/topics/hide", "topic=tele/чужой-датчик&tree=1")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("код ответа %d, ожидался %d", rec.Code, http.StatusSeeOther)
	}

	hidden, err := db.HiddenTopics(context.Background())
	if err != nil {
		t.Fatalf("HiddenTopics: %v", err)
	}
	if len(hidden) != 1 || hidden[0].Pattern != "tele/чужой-датчик" || !hidden[0].Tree {
		t.Fatalf("в скрытых %v, ожидался мастер-топик чужого датчика", hidden)
	}

	if rec := post(h, "/topics/show", "topic=tele/чужой-датчик"); rec.Code != http.StatusSeeOther {
		t.Fatalf("возврат ответил %d, ожидался %d", rec.Code, http.StatusSeeOther)
	}
	if hidden, _ := db.HiddenTopics(context.Background()); len(hidden) != 0 {
		t.Errorf("после возврата в скрытых осталось %v", hidden)
	}
}

// testHandler поднимает веб на временной базе.
func testHandler(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return db, Handler(log, db, "test", "", Status{})
}

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}
