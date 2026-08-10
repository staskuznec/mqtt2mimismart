package shClient

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

// PD коды протокола SHS
const (
	PD_REQUEST_ALL_DEVICES    uint8 = 4  // запрос всех устройств
	PD_SET_STATUS_TO_SERVER   uint8 = 5  // установка статуса на сервер
	PD_SET_STATUS_FROM_SERVER       = 7  // получение статуса от сервера
	PD_PING_MODULE                  = 15 // пинг модуля с состояниями
)

type ShClient struct {
	ctx                context.Context
	addr               string   // адрес сервера статистики
	conn               net.Conn // соединение с сервером
	key                string   // ключ для авторизации
	allowTranslateUDP  bool     // разрешить перевод в UDP
	macID              string   // mac адрес клиента
	initClientDefValue uint16   // значение по умолчанию для инициализации клиента
	initClientID       uint16   // идентификатор клиента для дальнейшей отправки пакетов на сервер
}

func NewShClient(ctx context.Context, addr string, key string, allowTranslateUDP bool, macID string) *ShClient {
	log.Debug().Str("mac", macID).Msg("NewShClient")
	if macID == "" {
		macID = randomMacID()
	}
	return &ShClient{
		ctx:                ctx,
		addr:               addr,
		key:                key,
		allowTranslateUDP:  allowTranslateUDP,
		initClientDefValue: 0x7ef,
		macID:              macID,
	}
}

func (shc *ShClient) Start() error {
	op := "shClient.Start"
	conn, err := net.Dial("tcp", shc.addr)
	if err != nil {
		return errors.Wrap(err, op)
	}
	shc.conn = conn

	shc.authorize()

	// Обрабатываем ответ XML и вычисляем initClientID
	if err := shc.handlerSHXML(); err != nil {
		return errors.Wrap(err, "handlerSHXML")
	}

	log.Info().Uint16("initClientID", shc.initClientID).Msg("ShClient started successfully")
	return nil
}

func (shc *ShClient) Close() error {
	if shc.conn != nil {
		return shc.conn.Close()
	}
	return nil
}

func randomMacID() string {
	src := rand.NewSource(time.Now().UnixNano())
	rnd := rand.New(src)
	macId := fmt.Sprintf("%02x%02x%02x%02x%02x%02x", rnd.Intn(256), rnd.Intn(256), rnd.Intn(256), rnd.Intn(256), rnd.Intn(256), rnd.Intn(256))
	return macId
}

func (shc *ShClient) authorize() {
	op := "shClient.authorize"
	log.Debug().Str("op", op).Msg("authorize")
	if shc.conn != nil {
		responseData, err := shc.read(16)
		if err != nil {
			log.Error().Err(err).Str("fn", op).Msg("read")
		} else {
			key := []byte(shc.key)
			block, err := aes.NewCipher(key)
			if err != nil {
				log.Error().Err(err).Str("fn", op).Msg("generate aes.NewCipher")
				return
			}
			cipherText := make([]byte, len(responseData))
			block.Encrypt(cipherText, responseData)
			_, err = shc.conn.Write(cipherText)
			if err != nil {
				log.Error().Err(err).Str("fn", op).Msg("write aes.NewCipher")
			}
		}
	}
}

// read получение ответа от сервера
func (shc *ShClient) read(size int) ([]byte, error) {
	op := "[SHC.read]"
	if shc.conn == nil {
		return nil, errors.New("connection is nil")
	}
	buf := make([]byte, size)
	i, err := io.ReadFull(shc.conn, buf)
	if err != nil {
		log.Error().Err(err).Str("fn", op).Msg("SHclient disconnected")
		return nil, err
	}
	if i != size {
		log.Error().Err(errors.New("read size error")).Str("fn", op).Msg("SHclient disconnected")
		return nil, errors.New("read size error")
	}

	return buf, nil
}

// handlerSHXML формирует запрос на получение logic.xml
// получаем xml логику (get-shc) и высчитываем ClientID для дальнейшей отправки пакетов на сервер
func (shc *ShClient) handlerSHXML() error {
	op := "shClient.handlerSHXML"
	log.Debug().Str("mac", shc.macID).Msg("Sending XML request")

	// Формируем и отправляем XML запрос
	xml := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<smart-house-commands>\n"
	if shc.allowTranslateUDP {
		xml += fmt.Sprintf("<get-shc retranslate-udp=\"yes\" mac-id=\"%s\"/>\n", shc.macID)
	} else {
		xml += fmt.Sprintf("<get-shc mac-id=\"%s\"/>\n", shc.macID)
	}
	xml += "</smart-house-commands>\n"

	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, uint32(len(xml)))
	data = append(data, []byte(xml)...)
	err := shc.send(data)
	if err != nil {
		return err
	}

	// ВАЖНО: Читаем ответ от сервера
	for i := 0; i < 3; i++ {
		// Читаем заголовок: 4 байта length + 6 байт header
		header, err := shc.read(10)
		if err != nil {
			log.Error().Err(err).Str("fn", op).Msg("Failed to read header")
			return errors.Wrap(err, "read header")
		}

		length := binary.LittleEndian.Uint32(header[0:4])
		shHead := string(header[4:10])

		log.Debug().
			Str("fn", op).
			Str("sh_head", shHead).
			Uint32("length", length).
			Msg("Received response")

		if shHead == "shcxml" {
			// Читаем 5 байт (4 CRC + 1 serverByte)
			crcAndByte, err := shc.read(5)
			if err != nil {
				log.Error().Err(err).Str("fn", op).Msg("Failed to read CRC and serverByte")
				return errors.Wrap(err, "read CRC and serverByte")
			}

			serverByte := crcAndByte[4] // последний байт
			shc.initClientID = shc.initClientDefValue - uint16(serverByte)

			log.Info().
				Uint16("initClientDefValue", shc.initClientDefValue).
				Uint8("serverByte", serverByte).
				Uint16("initClientID", shc.initClientID).
				Msg("ClientID initialized")

			// Читаем и пропускаем XML данные (length - 6 заголовок - 5 CRC+byte)
			xmlLen := int(length - 11)
			if xmlLen > 0 {
				xmlData, err := shc.read(xmlLen)
				if err != nil {
					log.Error().Err(err).Str("fn", op).Msg("Failed to read XML data")
					return errors.Wrap(err, "read XML data")
				}
				log.Debug().Int("xml_len", len(xmlData)).Msg("Cleared socket buffer from logic data")
			}

			return nil // Успешно обработали

		} else if shHead == "pkfail" {
			log.Error().Str("fn", op).Msg("Invalid key")
			return errors.New("invalid key")

		} else {
			// Пропускаем неизвестный пакет
			remainingLen := int(length - 6)
			if remainingLen > 0 {
				_, err := shc.read(remainingLen)
				if err != nil {
					log.Error().Err(err).Str("fn", op).Msg("Failed to skip unknown packet")
					return errors.Wrap(err, "skip unknown packet")
				}
			}
		}

		time.Sleep(100 * time.Millisecond) // Пауза перед следующей попыткой
	}

	return errors.New("failed to receive shcxml response after 3 attempts")
}

// allDeviceState запрос статус всех устройств
func (shc *ShClient) allDeviceState() {
	op := "[SHC.statusAllDevice]"
	clientID := uint16(0)
	id := uint16(0)
	subId := uint8(0)
	pd := PD_REQUEST_ALL_DEVICES
	length := uint16(0)
	var state []byte
	log.Debug().Str("op", op).Uint16("id", id).Uint8("subId", subId).Uint8("pd", pd).Uint16("length", length).Bytes("state", state)
	data := shc.packReceiveData(clientID, id, subId, pd, length, state)
	err := shc.send(data)
	if err != nil {
		return
	}
}

// packReceiveData пакуем данные в []byte для дальнейшей отправки на сервер
func (shc *ShClient) packReceiveData(clientID uint16, id uint16, subid uint8, pd uint8, length uint16, status []byte) []byte {
	op := "[SHC.packReceiveData]"
	p := ReceivePacket{
		ClientID: clientID,
		ID:       id,
		PD:       pd,
		Zero1:    0,
		Zero2:    0,
		SubID:    subid,
		Length:   length,
	}

	buf := new(bytes.Buffer)
	err := binary.Write(buf, binary.LittleEndian, &p)
	if err != nil {
		log.Error().Err(err).Str("fn", op).Msg("Error packReviveData")
	}
	// добавляем статус
	for i := 0; i < len(status); i++ {
		err := binary.Write(buf, binary.LittleEndian, status[i])
		if err != nil {
			log.Error().Err(err).Str("fn", op).Msg("Error packReviveData")
		}
	}

	return buf.Bytes()
}

// ============================================================================
// Высокоуровневые методы для работы с текстом и сенсорами
// ============================================================================

// SetStatusText отправляет текстовый статус на сервер (PD=5)
func (shc *ShClient) SetStatusText(id uint16, subid uint8, text string) error {
	op := "shClient.SetStatusText"
	if shc.conn == nil {
		return errors.New("connection is nil")
	}

	payload := []byte(text)
	data := shc.packReceiveData(shc.initClientID, id, subid, PD_SET_STATUS_TO_SERVER, uint16(len(payload)), payload)
	err := shc.send(data)
	if err != nil {
		return err
	}

	log.Debug().
		Str("op", op).
		Uint16("id", id).
		Uint8("subid", subid).
		Str("text", text).
		Msg("Sent text status")

	return nil
}

// PackStatusText формирует пакет текстового статуса БЕЗ отправки (для батчевой отправки)
func (shc *ShClient) PackStatusText(id uint16, subid uint8, text string) []byte {
	payload := []byte(text)
	return shc.packReceiveData(shc.initClientID, id, subid, PD_SET_STATUS_TO_SERVER, uint16(len(payload)), payload)
}

// SetSensor отправляет значение датчика на сервер (PD=5)
// floatValue преобразуется в uint16 fixed-point: raw = uint16(floatValue * 256)
func (shc *ShClient) SetSensor(id uint16, subid uint8, floatValue float64) error {
	op := "shClient.SetSensor"
	if shc.conn == nil {
		return errors.New("connection is nil")
	}

	// Преобразуем float в uint16 fixed-point (умножаем на 256)
	raw := uint16(floatValue * 256)

	// Упаковываем в 2 байта Little-Endian
	payload := make([]byte, 2)
	binary.LittleEndian.PutUint16(payload, raw)

	data := shc.packReceiveData(shc.initClientID, id, subid, PD_SET_STATUS_TO_SERVER, uint16(len(payload)), payload)
	err := shc.send(data)
	if err != nil {
		return err
	}

	log.Debug().
		Str("op", op).
		Uint16("id", id).
		Uint8("subid", subid).
		Float64("float_value", floatValue).
		Uint16("raw_value", raw).
		Msg("Sent sensor value")

	return nil
}

// PackSensor формирует пакет датчика БЕЗ отправки (для батчевой отправки)
func (shc *ShClient) PackSensor(id uint16, subid uint8, floatValue float64) []byte {
	// Преобразуем float в uint16 fixed-point (умножаем на 256)
	raw := uint16(floatValue * 256)

	// Упаковываем в 2 байта Little-Endian
	payload := make([]byte, 2)
	binary.LittleEndian.PutUint16(payload, raw)

	return shc.packReceiveData(shc.initClientID, id, subid, PD_SET_STATUS_TO_SERVER, uint16(len(payload)), payload)
}

// send отправляет данные на сервер (приватный метод с детальным логированием)
func (shc *ShClient) send(data []byte) error {
	if shc.conn == nil {
		return errors.New("connection is nil")
	}

	length, err := shc.conn.Write(data)
	if err != nil {
		log.Error().Err(err).Msg("send failed")
		return errors.Wrap(err, "send")
	}

	log.Debug().Int("bytes_sent", length).Msg("Data sent successfully")
	return nil
}

// WriteFull отправляет готовый пакет на сервер (публичный метод для батчевой отправки)
func (shc *ShClient) WriteFull(pkt []byte) error {
	return shc.send(pkt)
}

// DrainResponses читает и игнорирует ответы от сервера в течение заданного времени
// Используется после отправки пакетов, чтобы сервер не закрыл соединение преждевременно
func (shc *ShClient) DrainResponses(duration time.Duration) {
	op := "shClient.DrainResponses"
	if shc.conn == nil {
		return
	}

	deadline := time.Now().Add(duration)

	// Устанавливаем короткий таймаут чтения
	_ = shc.conn.SetReadDeadline(deadline)

	// Читаем всё что есть в буфере и игнорируем
	buf := make([]byte, 4096)
	totalRead := 0
	for time.Now().Before(deadline) {
		n, err := shc.conn.Read(buf)
		if err != nil || n == 0 {
			break
		}
		totalRead += n
	}

	// Сбрасываем дедлайн
	_ = shc.conn.SetReadDeadline(time.Time{})

	if totalRead > 0 {
		log.Debug().Str("op", op).Int("bytes_drained", totalRead).Msg("Drained server responses")
	}
}

// PackRaw формирует пакет с произвольной полезной нагрузкой БЕЗ отправки.
//
// Нужен для элементов, у которых состояние занимает не два байта датчика, а
// столько, сколько задано типом элемента. Виртуальная лампа, например, хранит
// состояние в одном байте: если отправить ей значение датчика (fixed-point
// 8.8), то единица превратится в байты 00 01, младший байт окажется нулём и
// лампа останется выключенной.
func (shc *ShClient) PackRaw(id uint16, subid uint8, payload []byte) []byte {
	return shc.packReceiveData(shc.initClientID, id, subid, PD_SET_STATUS_TO_SERVER, uint16(len(payload)), payload)
}
