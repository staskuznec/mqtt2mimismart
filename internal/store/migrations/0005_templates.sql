-- Пользовательские шаблоны устройств.
--
-- Встроенные едут в бинарнике и правке не подлежат: обновление шлюза не должно
-- ломать то, что уже развёрнуто на объекте. Загруженные лежат здесь и
-- перекрывают встроенные с тем же ключом — так модель можно поправить под свою
-- прошивку, не дожидаясь релиза.
CREATE TABLE templates (
    key        TEXT PRIMARY KEY,
    name       TEXT    NOT NULL,
    body_json  TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
