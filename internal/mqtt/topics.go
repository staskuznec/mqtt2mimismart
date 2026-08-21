package mqtt

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Ограничения снифера. Шина чужая, и что по ней ходит, мы заранее не знаем:
// на объекте с чужими устройствами топиков может оказаться сколько угодно.
const (
	// maxTopics — предел числа запоминаемых топиков. Превышение не роняет
	// приём, но новые топики перестают запоминаться, и об этом видно в вебе.
	maxTopics = 4096

	// maxPayloadKept — сколько байт нагрузки храним для показа. Полный JSON
	// от устройства редко длиннее, а держать в памяти чужие мегабайты незачем.
	maxPayloadKept = 1024
)

// Вид полезной нагрузки. Нужен веб-интерфейсу: для JSON он показывает дерево
// полей, из которого путь выбирается нажатием, а не набирается руками.
const (
	KindJSON   = "json"
	KindNumber = "number"
	KindText   = "text"
	KindEmpty  = "empty"
)

// TopicInfo — что известно про топик, который хоть раз проходил по шине.
type TopicInfo struct {
	Topic       string
	LastPayload []byte
	Truncated   bool // нагрузка была длиннее maxPayloadKept и обрезана при сохранении
	Kind        string
	Retained    bool
	Count       uint64
	FirstAt     time.Time
	LastAt      time.Time
}

// Hidden — правило, по которому топик не запоминается вовсе.
//
// Шина чужая: на объекте по ней ходят датчики, шлюзы и опросчики, к умному
// дому отношения не имеющие. Убрать такой топик из снифера мало — он вернётся
// со следующим сообщением, а публикуются они каждые несколько секунд.
type Hidden struct {
	// Pattern — точный топик либо мастер-топик устройства.
	Pattern string

	// Tree — Pattern считать мастер-топиком: скрыто и всё, что под ним.
	Tree bool
}

// covers сообщает, попадает ли топик под правило.
//
// Граница уровня обязательна: "shellies/shelly1" не должен утаскивать за
// собой "shellies/shelly1pm-a8", у которого просто общее начало строки.
func (h Hidden) covers(topic string) bool {
	if h.Pattern == topic {
		return true
	}
	return h.Tree && strings.HasPrefix(topic, h.Pattern+"/")
}

// registry — то, что видно на шине. Заполняется на каждом сообщении, читается
// веб-интерфейсом.
type registry struct {
	mu       sync.RWMutex
	topics   map[string]*TopicInfo
	overflow uint64 // сколько топиков не запомнили из-за предела
	hidden   []Hidden
}

func newRegistry() *registry {
	return &registry{topics: make(map[string]*TopicInfo)}
}

// observe запоминает очередное сообщение.
func (r *registry) observe(m Message) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Скрытое не запоминается вовсе, а не прячется при показе: снифер держит
	// последнюю нагрузку каждого топика, и место в нём кончается — предел
	// один на всю шину, и занимать его чужим опросчиком незачем.
	for _, h := range r.hidden {
		if h.covers(m.Topic) {
			return
		}
	}

	info, ok := r.topics[m.Topic]
	if !ok {
		if len(r.topics) >= maxTopics {
			r.overflow++
			return
		}
		info = &TopicInfo{Topic: m.Topic, FirstAt: m.At}
		r.topics[m.Topic] = info
	}

	payload := m.Payload
	truncated := false
	if len(payload) > maxPayloadKept {
		payload, truncated = payload[:maxPayloadKept], true
	}
	// Копия обязательна: срез приходит из библиотеки и живёт ровно до конца
	// обработчика, а мы храним его до следующего сообщения в этом топике.
	info.LastPayload = append([]byte(nil), payload...)

	info.Truncated = truncated
	info.Kind = kindOf(m.Payload)
	info.Retained = m.Retained
	info.Count++
	info.LastAt = m.At
}

// Topics отдаёт снимок увиденного, свежие сверху.
func (r *registry) Topics() []TopicInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]TopicInfo, 0, len(r.topics))
	for _, info := range r.topics {
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
}

// Topic отдаёт сведения об одном топике.
func (r *registry) Topic(topic string) (TopicInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, ok := r.topics[topic]
	if !ok {
		return TopicInfo{}, false
	}
	return *info, true
}

// forgetAll очищает снифер. Нужен кнопке «очистить» в вебе: после настройки
// связок список удобно обнулить и посмотреть, что реально ходит сейчас.
func (r *registry) forgetAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.topics = make(map[string]*TopicInfo)
	r.overflow = 0
}

// forget убирает из снифера то, что подходит под правило, и отвечает, сколько
// топиков убрал. Правило при этом не запоминается: вернуть топик может любое
// следующее сообщение — это и нужно, когда убирают остаток от устройства,
// которого на шине уже нет.
func (r *registry) forget(h Hidden) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := 0
	for topic := range r.topics {
		if h.covers(topic) {
			delete(r.topics, topic)
			n++
		}
	}
	return n
}

// setHidden заменяет список скрытого и сразу убирает из снифера то, что под
// него попало: иначе скрытый топик оставался бы на странице до перезапуска.
func (r *registry) setHidden(rules []Hidden) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.hidden = append([]Hidden(nil), rules...)
	for topic := range r.topics {
		for _, h := range r.hidden {
			if h.covers(topic) {
				delete(r.topics, topic)
				break
			}
		}
	}
}

// stats отдаёт размер снифера и число незапомненных топиков.
func (r *registry) stats() (known int, overflow uint64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.topics), r.overflow
}

// kindOf определяет вид нагрузки.
//
// Порядок проверок важен: "123" разбирается и как JSON, и как число, но
// показывать его деревом полей бессмысленно, поэтому число проверяется первым.
func kindOf(payload []byte) string {
	s := strings.TrimSpace(string(payload))
	if s == "" {
		return KindEmpty
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return KindNumber
	}
	// Составной JSON — только объект или массив: для дерева полей годятся
	// именно они.
	if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
		if json.Valid([]byte(s)) {
			return KindJSON
		}
	}
	return KindText
}
