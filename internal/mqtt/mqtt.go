// Package mqtt — соединение с брокером и снифер шины.
//
// Кроме приёма и публикации здесь живёт то, ради чего затевался веб: реестр
// увиденных топиков. Шлюз в режиме обучения подписан на всё подряд, и в
// разделе «Топики» видно, что реально ходит по шине — с последней нагрузкой,
// её видом и частотой. Связка настраивается выбором из этого списка, а не
// набором топика руками.
package mqtt

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

const (
	// LearningFilter — подписка на всю шину. Именно она наполняет снифер.
	LearningFilter = "#"

	// connectTimeout — сколько ждём первого подключения к брокеру.
	connectTimeout = 10 * time.Second

	// opTimeout — предел на подписку и публикацию.
	opTimeout = 5 * time.Second

	// reconnectInterval — потолок паузы между попытками переподключения.
	// Само переподключение делает библиотека, от нас нужен только предел.
	reconnectInterval = 30 * time.Second

	// messageBuffer — запас в очереди сообщений. На шине умного дома их
	// немного, но при подписке на retained-топики они прилетают пачкой.
	messageBuffer = 1024

	// disconnectQuiesce — сколько даём библиотеке дописать начатое при
	// отключении. Ровно столько она и ждёт, даже когда соединения нет, так
	// что величина видна на каждой остановке демона.
	disconnectQuiesce = 250 * time.Millisecond
)

// Config — параметры подключения к брокеру. Приходят из базы, вводятся в вебе.
type Config struct {
	Addr     string // host:port брокера
	User     string
	Password string
	ClientID string // пусто — соберётся из имени хоста
}

// Message — сообщение с шины.
type Message struct {
	Topic    string
	Payload  []byte
	Retained bool
	QoS      byte
	At       time.Time
}

// Status — снимок состояния для веб-интерфейса.
type Status struct {
	Connected     bool
	ClientID      string
	Since         time.Time
	LastError     string
	Received      uint64
	Published     uint64
	Dropped       uint64 // сообщения, не попавшие в очередь при остановке
	Connects      uint64
	KnownTopics   int
	TopicOverflow uint64
	LastMessageAt time.Time
}

// Client — соединение с брокером.
type Client struct {
	log      *slog.Logger
	cfg      Config
	client   paho.Client
	messages chan Message
	topics   *registry

	mu      sync.Mutex
	status  Status
	filters map[string]byte // на что подписаны; восстанавливается после обрыва
}

// New собирает клиента. Соединение не открывается: этим занимается Run.
func New(cfg Config, log *slog.Logger) (*Client, error) {
	if strings.TrimSpace(cfg.Addr) == "" {
		return nil, fmt.Errorf("mqtt: не задан адрес брокера")
	}
	if cfg.ClientID == "" {
		cfg.ClientID = defaultClientID()
	}

	c := &Client{
		log:      log,
		cfg:      cfg,
		messages: make(chan Message, messageBuffer),
		topics:   newRegistry(),
		filters:  make(map[string]byte),
		status:   Status{ClientID: cfg.ClientID},
	}
	return c, nil
}

// Messages отдаёт канал сообщений. Читатель должен быть один.
func (c *Client) Messages() <-chan Message { return c.messages }

// Topics отдаёт содержимое снифера.
func (c *Client) Topics() []TopicInfo { return c.topics.Topics() }

// Topic отдаёт сведения об одном топике — из него редактор связки берёт
// последнюю нагрузку для панели «Проба».
func (c *Client) Topic(topic string) (TopicInfo, bool) { return c.topics.Topic(topic) }

// ForgetTopics очищает снифер.
func (c *Client) ForgetTopics() { c.topics.forgetAll() }

// ForgetTopic убирает из снифера один топик или мастер-топик целиком и
// отвечает, сколько строк убрал.
func (c *Client) ForgetTopic(h Hidden) int { return c.topics.forget(h) }

// SetHidden задаёт, что снифер не запоминает. Список приходит из базы и
// применяется целиком: правило снимают там же, где ставят.
func (c *Client) SetHidden(rules []Hidden) { c.topics.setHidden(rules) }

// Status возвращает снимок состояния.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()

	st := c.status
	st.KnownTopics, st.TopicOverflow = c.topics.stats()
	return st
}

// Run подключается к брокеру и держит соединение до отмены контекста.
//
// Переподключением занимается сама библиотека, поэтому здесь нет цикла с
// паузами: достаточно дождаться отмены и корректно отключиться.
func (c *Client) Run(ctx context.Context) error {
	defer close(c.messages)

	opts := paho.NewClientOptions().
		AddBroker("tcp://" + c.cfg.Addr).
		SetClientID(c.cfg.ClientID).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(reconnectInterval).
		// Повтор и для первого подключения тоже: шлюз должен подниматься,
		// даже если брокер ещё не запустился. Веб при этом работает и честно
		// показывает, что связи нет.
		SetConnectRetry(true).
		SetConnectRetryInterval(reconnectInterval).
		SetConnectTimeout(connectTimeout).
		// Сессия чистая: подписки мы восстанавливаем сами в обработчике
		// подключения, а копить очередь на брокере для шлюза вредно —
		// после долгого простоя он получил бы лавину устаревших состояний.
		SetCleanSession(true).
		// Порядок сообщений сохраняем. С false библиотека вызывает обработчик
		// в отдельной горутине на каждое сообщение, и два состояния одного
		// реле, пришедшие подряд, попадают в очередь как повезёт. Дальше их
		// разбирает отсев повторов: в кэш ложится устаревшее значение, а
		// правильное отбрасывается как «уже отправляли», — и элемент залипает
		// до следующей публикации. Gen1 публикует состояние только при
		// изменении, так что «до следующей» означает «пока кто-нибудь не
		// щёлкнет».
		//
		// Плата за порядок: обработчик обязан не блокироваться. Наш только
		// копирует сообщение в очередь, а когда она полна — тормозит приём,
		// то есть придерживает и подтверждения публикаций. Это заметно, но
		// честно: очередь на тысячу сообщений заполняется, только если умный
		// дом перестал принимать, и перемешанные состояния в этот момент
		// сделали бы хуже.
		SetOrderMatters(true).
		SetOnConnectHandler(c.onConnect).
		SetConnectionLostHandler(c.onConnectionLost).
		SetDefaultPublishHandler(c.onMessage(ctx))

	if c.cfg.User != "" {
		opts.SetUsername(c.cfg.User)
		opts.SetPassword(c.cfg.Password)
	}

	c.client = paho.NewClient(opts)
	token := c.client.Connect()

	// Ждём либо первого подключения, либо отмены. С SetConnectRetry токен
	// завершается только успехом, поэтому таймаут здесь не нужен — незачем
	// объявлять ошибкой то, что библиотека сама повторит.
	go func() {
		token.Wait()
		if err := token.Error(); err != nil {
			c.setError(err)
			c.log.Error("подключение к брокеру", "err", err)
		}
	}()

	<-ctx.Done()

	c.log.Info("отключаемся от брокера")
	c.client.Disconnect(uint(disconnectQuiesce.Milliseconds()))
	return ctx.Err()
}

// Subscribe подписывается на фильтр топиков и запоминает его, чтобы
// восстановить после переподключения.
func (c *Client) Subscribe(filter string, qos byte) error {
	c.mu.Lock()
	c.filters[filter] = qos
	client := c.client
	c.mu.Unlock()

	if client == nil || !client.IsConnected() {
		// Не ошибка: подписка запомнена и уедет на брокер при подключении.
		c.log.Debug("подписка отложена до подключения", "filter", filter)
		return nil
	}

	token := client.Subscribe(filter, qos, nil)
	if !token.WaitTimeout(opTimeout) {
		return fmt.Errorf("mqtt: подписка на %q: истекло время ожидания", filter)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("mqtt: подписка на %q: %w", filter, err)
	}
	c.log.Info("подписка оформлена", "filter", filter, "qos", qos)
	return nil
}

// Unsubscribe снимает подписку.
func (c *Client) Unsubscribe(filter string) error {
	c.mu.Lock()
	delete(c.filters, filter)
	client := c.client
	c.mu.Unlock()

	if client == nil || !client.IsConnected() {
		return nil
	}
	token := client.Unsubscribe(filter)
	if !token.WaitTimeout(opTimeout) {
		return fmt.Errorf("mqtt: снятие подписки с %q: истекло время ожидания", filter)
	}
	return token.Error()
}

// Publish отправляет сообщение в топик.
//
// Флаг retain оставлен на усмотрение вызывающего, но для командных топиков он
// недопустим: брокер отдаст сохранённую команду устройству при следующем его
// подключении, и реле щёлкнет само. Проверку делает редактор связок.
func (c *Client) Publish(topic string, payload []byte, qos byte, retain bool) error {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	if client == nil || !client.IsConnected() {
		return fmt.Errorf("mqtt: нет соединения с брокером")
	}

	token := client.Publish(topic, qos, retain, payload)
	if !token.WaitTimeout(opTimeout) {
		return fmt.Errorf("mqtt: публикация в %q: истекло время ожидания", topic)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("mqtt: публикация в %q: %w", topic, err)
	}

	c.mu.Lock()
	c.status.Published++
	c.mu.Unlock()

	c.log.Debug("опубликовано", "topic", topic, "qos", qos, "retain", retain,
		"payload", string(payload))
	return nil
}

// onConnect восстанавливает подписки. Вызывается и при первом подключении, и
// после каждого переподключения — с чистой сессией брокер о них не помнит.
func (c *Client) onConnect(client paho.Client) {
	c.mu.Lock()
	c.status.Connected = true
	c.status.Since = time.Now()
	c.status.LastError = ""
	c.status.Connects++
	filters := make(map[string]byte, len(c.filters))
	for f, q := range c.filters {
		filters[f] = q
	}
	c.mu.Unlock()

	c.log.Info("брокер на связи", "addr", c.cfg.Addr, "client_id", c.cfg.ClientID)

	for filter, qos := range filters {
		token := client.Subscribe(filter, qos, nil)
		if !token.WaitTimeout(opTimeout) {
			c.log.Error("восстановление подписки: истекло время ожидания", "filter", filter)
			continue
		}
		if err := token.Error(); err != nil {
			c.log.Error("восстановление подписки", "filter", filter, "err", err)
			continue
		}
		c.log.Debug("подписка восстановлена", "filter", filter, "qos", qos)
	}
}

func (c *Client) onConnectionLost(_ paho.Client, err error) {
	c.setError(err)
	c.log.Error("связь с брокером потеряна", "err", err)
}

// onMessage раскладывает сообщение в снифер и в очередь потребителя.
func (c *Client) onMessage(ctx context.Context) paho.MessageHandler {
	return func(_ paho.Client, m paho.Message) {
		msg := Message{
			Topic: m.Topic(),
			// Копия обязательна: библиотека переиспользует буфер, а сообщение
			// живёт в очереди дольше вызова обработчика.
			Payload:  append([]byte(nil), m.Payload()...),
			Retained: m.Retained(),
			QoS:      m.Qos(),
			At:       time.Now(),
		}

		c.topics.observe(msg)

		c.mu.Lock()
		c.status.Received++
		c.status.LastMessageAt = msg.At
		c.mu.Unlock()

		select {
		case c.messages <- msg:
			return
		default:
		}

		// Очередь заполнена. Блокируемся, а не выбрасываем: пропущенное
		// состояние устройства выглядит как «шлюз иногда не срабатывает».
		c.log.Warn("очередь сообщений заполнена, приём приостановлен",
			"topic", msg.Topic, "buffer", cap(c.messages))
		select {
		case c.messages <- msg:
		case <-ctx.Done():
			c.mu.Lock()
			c.status.Dropped++
			c.mu.Unlock()
		}
	}
}

func (c *Client) setError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Connected = false
	if err != nil {
		c.status.LastError = err.Error()
	}
}

// defaultClientID собирает идентификатор клиента из имени хоста.
//
// Он обязан быть уникальным на брокере: два клиента с одним идентификатором
// выбивают друг друга из сети по очереди, и выглядит это как мигающая связь.
func defaultClientID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return "mqtt2mimismart-" + host
}
