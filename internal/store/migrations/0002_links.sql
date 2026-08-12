-- Устройства и связки.
--
-- Устройство — это группа связок с общим префиксом топика. Нужно оно ради двух
-- вещей: свернуть десяток связок в одну карточку в интерфейсе и знать, кому не
-- доверять данные, когда устройство пропало со связи.
CREATE TABLE devices (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    topic_prefix TEXT    NOT NULL DEFAULT '', -- "shellies/shelly25-A1B2C3"
    model        TEXT    NOT NULL DEFAULT '', -- из announce: "SHSW-25"
    template     TEXT    NOT NULL DEFAULT '', -- имя применённого шаблона
    online       INTEGER NOT NULL DEFAULT 0,  -- по признаку присутствия (LWT)
    last_seen    INTEGER,
    created_at   INTEGER NOT NULL
);

CREATE UNIQUE INDEX devices_topic_prefix ON devices (topic_prefix)
    WHERE topic_prefix <> '';

-- Связка — одно направление: либо сообщение с шины кладётся в элемент, либо
-- изменение элемента публикуется в топик.
--
-- Имена столбцов подобраны в обход зарезервированных слов SQL: values и offset
-- ими являются, поэтому values_json и offset_value.
CREATE TABLE links (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id INTEGER REFERENCES devices (id) ON DELETE CASCADE,
    name      TEXT    NOT NULL DEFAULT '',
    enabled   INTEGER NOT NULL DEFAULT 1,
    direction TEXT    NOT NULL, -- in | out

    -- Сторона MQTT. Для in это фильтр подписки с "+" и "#",
    -- для out — конкретный топик публикации.
    topic  TEXT    NOT NULL,
    qos    INTEGER NOT NULL DEFAULT 0,
    retain INTEGER NOT NULL DEFAULT 0,

    -- Извлечение значения из полезной нагрузки (только in).
    extract      TEXT NOT NULL DEFAULT 'raw', -- raw | json
    extract_path TEXT NOT NULL DEFAULT '',    -- "relays.0.ison"

    -- Таблица перевода значений: {"on":"1","off":"0"}. Пусто — оставить как есть.
    values_json TEXT NOT NULL DEFAULT '',

    -- Числовые поправки (только in). Ноль в scale означает множитель 1.
    scale        REAL NOT NULL DEFAULT 0,
    offset_value REAL NOT NULL DEFAULT 0,

    -- Элемент умного дома.
    target_id    INTEGER NOT NULL,
    target_subid INTEGER NOT NULL,

    -- Форма значения: encode для in, decode для out.
    encode TEXT NOT NULL DEFAULT '', -- byte | sensor | text
    decode TEXT NOT NULL DEFAULT '', -- byte | sensor | text | lamp
    unit   TEXT NOT NULL DEFAULT '', -- приписка к тексту, например " Вт"

    -- NULL означает «без округления». Ноль здесь занят и значит округление
    -- до целого, иначе 42.3 молча превращалось бы в 42.
    precision INTEGER,

    only_changed INTEGER NOT NULL DEFAULT 1,

    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX links_device ON links (device_id);
CREATE INDEX links_direction ON links (direction);
CREATE INDEX links_target ON links (target_id, target_subid);
