package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/staskuznec/mqtt2mimismart/internal/link"
	"github.com/staskuznec/mqtt2mimismart/internal/mqtt"
)

// Уборка на странице «Топики».
//
// Шлюз подписан на всю шину, и на объекте с чужими устройствами список тонет в
// том, что к умному дому отношения не имеет: опросчик счётчиков, чужой
// Zigbee-мост, отладочная болтовня чьей-то прошивки. Отсюда два действия.
//
// «Убрать» вычёркивает строку из снифера сейчас — этого хватает остаткам от
// устройства, которого на шине уже нет: возвращать их некому. «Скрыть»
// заносит топик в базу, и снифер перестаёт его запоминать вовсе, — это для
// живого чужого устройства, которое иначе вернётся через секунду.

// forgetTopic убирает топик или мастер-топик из снифера.
func (s *server) forgetTopic(w http.ResponseWriter, r *http.Request) {
	pattern, tree, err := topicTarget(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if msg := s.topicInUse(r.Context(), pattern, tree, "убрать"); msg != "" {
		s.topicsError(w, r, msg)
		return
	}

	if s.status.ForgetTopic != nil {
		s.status.ForgetTopic(mqtt.Hidden{Pattern: pattern, Tree: tree})
	}
	s.redirect(w, r, "/topics")
}

// hideTopic заносит топик в скрытые: он не вернётся и с новым сообщением.
func (s *server) hideTopic(w http.ResponseWriter, r *http.Request) {
	pattern, tree, err := topicTarget(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if msg := s.topicInUse(r.Context(), pattern, tree, "скрыть"); msg != "" {
		s.topicsError(w, r, msg)
		return
	}

	if err := s.db.HideTopic(r.Context(), pattern, tree); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.applyHidden(r)
	s.redirect(w, r, "/topics")
}

// showTopic возвращает скрытый топик: снифер снова начнёт его запоминать.
func (s *server) showTopic(w http.ResponseWriter, r *http.Request) {
	pattern := strings.TrimSpace(r.PostFormValue("topic"))
	if pattern == "" {
		http.Error(w, "не указан топик", http.StatusBadRequest)
		return
	}
	if err := s.db.ShowTopic(r.Context(), pattern); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.applyHidden(r)
	s.redirect(w, r, "/topics")
}

// applyHidden доводит правку списка до снифера.
//
// База — источник правды, но решает, что запоминать, снифер в памяти. Без
// этого скрытый топик оставался бы на странице до перезапуска службы.
func (s *server) applyHidden(r *http.Request) {
	if s.status.ApplyHidden == nil {
		return
	}
	if err := s.status.ApplyHidden(r.Context()); err != nil {
		s.log.Error("скрытые топики не применены", "err", err)
	}
}

// topicTarget читает из формы, что именно убирают.
func topicTarget(r *http.Request) (pattern string, tree bool, err error) {
	pattern = strings.TrimSpace(r.PostFormValue("topic"))
	if pattern == "" {
		return "", false, errors.New("не указан топик")
	}
	return pattern, r.PostFormValue("tree") != "", nil
}

// topicsError возвращает на страницу с объяснением, почему не вышло.
func (s *server) topicsError(w http.ResponseWriter, r *http.Request, msg string) {
	s.redirect(w, r, "/topics?error="+url.QueryEscape(msg))
}

// topicInUse отвечает объяснением, если на топике держатся связки, и пустой
// строкой, если убирать его можно.
//
// Топик, с которого работает связка, убирают только по ошибке: на странице он
// выглядит так же, как чужой, а последствия разные — связка останется без
// источника и молча перестанет доносить значения. Поэтому не даём, а не
// спрашиваем «уверены?»: сначала связка, потом топик.
func (s *server) topicInUse(ctx context.Context, pattern string, tree bool, verb string) string {
	all, err := s.db.Links(ctx)
	if err != nil {
		s.log.Error("проверка связок на топике", "err", err)
		return ""
	}

	var names []string
	for _, l := range all {
		if topicCovered(pattern, tree, l.Topic) {
			names = append(names, l.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}

	what := "Топик"
	if tree {
		what = "Мастер-топик"
	}
	// Показываем три имени: по ним понятно, о каком устройстве речь, а полный
	// список из двух десятков связок в строку всё равно не читается.
	shown := names
	tail := ""
	if len(shown) > 3 {
		shown, tail = shown[:3], " и другие"
	}
	return fmt.Sprintf("%s %s не %s: на нём работают связки — %s%s. Сначала удалите их в разделе «Связки».",
		what, pattern, verb, strings.Join(shown, ", "), tail)
}

// topicCovered сообщает, попадает ли топик связки под уборку.
//
// Сравнением строк тут не обойтись: связка бывает подписана фильтром с
// подстановкой, и "shellies/+/relay/0" ловит топик, которого в самом фильтре
// не видно. Мастер-топик режем по границе уровня — "shellies/shelly1" не
// должен утащить за собой "shellies/shelly1pm-a8".
func topicCovered(pattern string, tree bool, linkTopic string) bool {
	if linkTopic == pattern {
		return true
	}
	if tree {
		return strings.HasPrefix(linkTopic, pattern+"/")
	}
	return link.Matches(linkTopic, pattern)
}
