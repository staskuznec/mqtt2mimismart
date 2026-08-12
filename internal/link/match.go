package link

import "strings"

// Сопоставление топика с фильтром подписки MQTT.
//
// Правила заданы стандартом, и вольничать с ними нельзя: связка, которая
// ловит лишнее, приводит к записи чужого значения в элемент умного дома.
//
//	"+"  заменяет ровно один уровень
//	"#"  заменяет ноль или больше уровней и допустим только последним
//
// Отдельное правило про топики, начинающиеся с "$": подстановочные знаки их не
// захватывают. Иначе подписка "#" в режиме обучения втянула бы служебную
// статистику брокера ($SYS), и снифер утонул бы в ней.
const (
	levelWildcard = "+"
	multiWildcard = "#"
	separator     = "/"
	systemPrefix  = "$"
)

// Matches сообщает, подходит ли топик под фильтр.
func Matches(filter, topic string) bool {
	if filter == "" || topic == "" {
		return false
	}
	if filter == topic {
		return true
	}

	filterLevels := strings.Split(filter, separator)
	topicLevels := strings.Split(topic, separator)

	// Служебные топики брокера подстановочными знаками не ловятся.
	if strings.HasPrefix(topicLevels[0], systemPrefix) &&
		(filterLevels[0] == multiWildcard || filterLevels[0] == levelWildcard) {
		return false
	}

	for i, level := range filterLevels {
		if level == multiWildcard {
			// "#" обязан быть последним; всё, что после него, — ошибка в
			// фильтре, и совпадением считать её нельзя.
			return i == len(filterLevels)-1
		}
		if i >= len(topicLevels) {
			return false
		}
		if level != levelWildcard && level != topicLevels[i] {
			return false
		}
	}

	// Фильтр кончился: топик подходит, только если кончился тоже.
	return len(filterLevels) == len(topicLevels)
}

// ValidFilter проверяет фильтр подписки.
func ValidFilter(filter string) bool {
	if filter == "" {
		return false
	}
	levels := strings.Split(filter, separator)
	for i, level := range levels {
		switch {
		case level == multiWildcard && i != len(levels)-1:
			return false
		case len(level) > 1 && strings.Contains(level, multiWildcard):
			return false
		case len(level) > 1 && strings.Contains(level, levelWildcard):
			return false
		}
	}
	return true
}

// ValidTopic проверяет топик для публикации: подстановочных знаков в нём быть
// не должно, публикуют всегда в конкретный топик.
func ValidTopic(topic string) bool {
	return topic != "" &&
		!strings.Contains(topic, multiWildcard) &&
		!strings.Contains(topic, levelWildcard)
}

// Captures достаёт значения, попавшие под подстановочные знаки фильтра.
//
// Нужны шаблонам устройств: фильтр "shellies/+/relay/+" на топике
// "shellies/shelly25-A1B2C3/relay/0" даёт ["shelly25-A1B2C3", "0"], и по ним
// связка понимает, к какому устройству и каналу относится сообщение.
func Captures(filter, topic string) []string {
	if !Matches(filter, topic) {
		return nil
	}

	filterLevels := strings.Split(filter, separator)
	topicLevels := strings.Split(topic, separator)

	var out []string
	for i, level := range filterLevels {
		switch level {
		case levelWildcard:
			out = append(out, topicLevels[i])
		case multiWildcard:
			// "#" забирает весь остаток одной строкой, включая пустой.
			out = append(out, strings.Join(topicLevels[i:], separator))
			return out
		}
	}
	return out
}
