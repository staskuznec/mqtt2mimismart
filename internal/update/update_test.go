package update

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
	c := New("v1.0.0", nil)
	info := c.Info()
	if info.Current != "v1.0.0" || info.Available {
		t.Errorf("до проверки: %+v", info)
	}
}

// Адрес шлюза в файле вкладки при обновлении должен сохраняться: установщик
// мог подставить туда порт или другой подкаталог.
func TestGatewayURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"подкаталог", `    var GATEWAY_URL = "/mqtt/";`, "/mqtt/"},
		{"прямой порт", `var GATEWAY_URL = "http://192.168.20.10:8080/";`, "http://192.168.20.10:8080/"},
		{"свой подкаталог", `var GATEWAY_URL = "/шлюз/";`, "/шлюз/"},
		{"нет адреса", `var OTHER = "x";`, ""},
		{"обрезано", `var GATEWAY_URL = "/mqtt/`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gatewayURL(tc.body); got != tc.want {
				t.Errorf("gatewayURL() = %q, ожидалось %q", got, tc.want)
			}
		})
	}
}

// Пока идёт переезд, свежий релиз может лежать только в одном источнике.
// Шлюз берёт самую новую версию, при равной — первый источник, и качает
// оттуда же, где её нашёл, со своей учётной записью.
func TestCheckPicksNewestSource(t *testing.T) {
	var auth string
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok {
			auth = u + ":" + p
		}
		_, _ = io.WriteString(w, giteaVersion+"\n")
	}))
	defer gitea.Close()
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"tag_name":"`+githubVersion+`","html_url":"https://github.com/x"}`)
	}))
	defer github.Close()

	check := func() Info {
		c := New("v0.13.5", nil)
		c.sources = []source{
			{name: "gitea", latest: gitea.URL, plain: true, download: gitea.URL + "/", user: "updater", token: "secret"},
			{name: "github", latest: github.URL, download: github.URL + "/"},
		}
		return c.Check(context.Background())
	}

	giteaVersion, githubVersion = "v0.14.0", "v0.14.0"
	if info := check(); info.Source != "gitea" || !info.Available || info.URL != "" {
		t.Errorf("равные версии: %+v, ожидался свой git без ссылки на страницу", info)
	}
	if auth != "updater:secret" {
		t.Errorf("учётная запись на свой git ушла как %q", auth)
	}

	giteaVersion, githubVersion = "v0.14.0", "v0.14.1"
	if info := check(); info.Source != "github" || info.Latest != "v0.14.1" {
		t.Errorf("на GitHub новее: %+v", info)
	}

	giteaVersion = "<html>login</html>"
	if info := check(); info.Source != "github" || info.Error != "" {
		t.Errorf("свой git ответил мусором: %+v, ожидался GitHub без ошибки", info)
	}
}

var giteaVersion, githubVersion string

// Gitea без доступа отправляет на страницу входа со статусом 200. Такая
// страница не должна сойти ни за номер версии, ни за файл релиза.
func TestLoginRedirectIsNoAccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/login" {
			http.Redirect(w, r, "/user/login", http.StatusSeeOther)
			return
		}
		_, _ = io.WriteString(w, "v9.9.9")
	}))
	defer srv.Close()

	c := New("v0.13.5", nil)
	s := source{name: "gitea", latest: srv.URL + "/latest", plain: true, download: srv.URL + "/"}
	c.sources = []source{s}
	if info := c.Check(context.Background()); info.Latest != "" || info.Error == "" {
		t.Errorf("страница входа сошла за версию: %+v", info)
	}
	if _, err := c.download(context.Background(), s, srv.URL+"/v9.9.9/SHA256SUMS"); err == nil {
		t.Error("страница входа сошла за файл релиза")
	}
}
