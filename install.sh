#!/bin/sh
# Установка и обновление шлюза mqtt2mimismart.
#
#   curl -fsSL https://raw.githubusercontent.com/staskuznec/mqtt2mimismart/main/install.sh | sh
#
# Скрипт ставит бинарник в /opt/mqtt2mimismart, заводит службу systemd и
# запускает её. Повторный запуск обновляет: база и настройки лежат отдельно,
# в /var/lib/mqtt2mimismart, и не трогаются.
#
# POSIX sh намеренно: на сервере статистики bash может и не стоять.
set -eu

REPO="staskuznec/mqtt2mimismart"
BIN_DIR="${BIN_DIR:-/opt/mqtt2mimismart}"
STATE_DIR="${STATE_DIR:-/var/lib/mqtt2mimismart}"
SERVICE="mqtt2mimismart"
ADDR="${ADDR:-0.0.0.0:8080}"

say()  { printf '%s\n' "$*"; }
die()  { printf 'ошибка: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "нужны права root: запустите через sudo"

# --- диалог с человеком ----------------------------------------------------
#
# Скрипт обычно запускают через "curl … | sh", и тогда стандартный ввод занят
# самим скриптом: обычный read съел бы его текст вместо ответа. Поэтому все
# вопросы читаются напрямую с терминала.
if [ -r /dev/tty ] && [ -w /dev/tty ]; then
  INTERACTIVE=yes
else
  INTERACTIVE=no
fi

ask() { # ask "вопрос" "по умолчанию"
  _ans=""
  if [ "$INTERACTIVE" = yes ]; then
    printf '%s' "$1" > /dev/tty
    IFS= read -r _ans < /dev/tty || _ans=""
  fi
  [ -n "$_ans" ] || _ans="$2"
  printf '%s' "$_ans"
}

ask_secret() { # ask_secret "вопрос" — ввод не показывается на экране
  _sec=""
  if [ "$INTERACTIVE" = yes ]; then
    printf '%s' "$1" > /dev/tty
    stty -echo < /dev/tty 2>/dev/null || true
    IFS= read -r _sec < /dev/tty || _sec=""
    stty echo < /dev/tty 2>/dev/null || true
    printf '\n' > /dev/tty
  fi
  printf '%s' "$_sec"
}

# --- какая платформа -------------------------------------------------------
case "$(uname -m)" in
  x86_64|amd64)   SUFFIX="linux-amd64" ;;
  aarch64|arm64)  SUFFIX="linux-arm64" ;;
  armv7l|armv6l)  SUFFIX="linux-armv7" ;;
  *) die "архитектура $(uname -m) не поддерживается" ;;
esac

# --- какая версия ----------------------------------------------------------
VERSION="${VERSION:-}"
if [ -z "$VERSION" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
  [ -n "$VERSION" ] || die "не удалось узнать последнюю версию"
fi
say "Версия: $VERSION, платформа: $SUFFIX"

BASE="https://github.com/$REPO/releases/download/$VERSION"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

# --- скачиваем и сверяем ---------------------------------------------------
say "Скачиваем…"
curl -fsSL "$BASE/mqtt2mimismart-$SUFFIX" -o "$TMP/gateway" || die "не удалось скачать бинарник"

# Сверка суммы обязательна: дальше этот файл запускается как служба.
if curl -fsSL "$BASE/SHA256SUMS" -o "$TMP/SHA256SUMS" 2>/dev/null; then
  want=$(sed -n "s/^\([0-9a-f]\{64\}\)  *mqtt2mimismart-$SUFFIX\$/\1/p" "$TMP/SHA256SUMS" | head -1)
  [ -n "$want" ] || die "в SHA256SUMS нет строки для mqtt2mimismart-$SUFFIX"
  if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$TMP/gateway" | cut -d' ' -f1)
  else
    got=$(shasum -a 256 "$TMP/gateway" | cut -d' ' -f1)
  fi
  [ "$want" = "$got" ] || die "контрольная сумма не сошлась — файл повреждён или подменён"
  say "Контрольная сумма сошлась."
else
  die "не удалось скачать SHA256SUMS — без сверки не ставим"
fi

chmod 0755 "$TMP/gateway"

# --- MQTT-брокер -----------------------------------------------------------
#
# Без брокера шлюзу не с чем работать вовсе, поэтому ставим его здесь же.
# Пропустить можно переменной SKIP_MOSQUITTO=1 — например, когда брокер живёт
# на другой машине.
MQTT_USER_DEVICES="devices"
MQTT_USER_GATEWAY="gateway"
MQTT_DEVICES_PASS=""
MQTT_GATEWAY_PASS=""
MQTT_MODE="${MQTT_MODE:-}" # anon | auth

install_mosquitto() {
  if command -v apt-get >/dev/null 2>&1; then
    say "Ставим mosquitto через apt…"
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq mosquitto mosquitto-clients
  elif command -v apk >/dev/null 2>&1; then
    say "Ставим mosquitto через apk…"
    apk add --no-cache mosquitto mosquitto-clients
  elif command -v dnf >/dev/null 2>&1; then
    say "Ставим mosquitto через dnf…"
    dnf install -y mosquitto
  elif command -v yum >/dev/null 2>&1; then
    say "Ставим mosquitto через yum…"
    yum install -y mosquitto
  else
    return 1
  fi
}

setup_mosquitto() {
  if [ "${SKIP_MOSQUITTO:-}" = "1" ]; then
    say "Брокер пропущен по SKIP_MOSQUITTO=1."
    return 0
  fi

  if command -v mosquitto >/dev/null 2>&1; then
    say "Брокер mosquitto уже установлен."
  else
    say ""
    say "MQTT-брокер не найден. Без него шлюзу не с чем работать:"
    say "устройства публикуют состояния именно в него."
    ans=$(ask "Установить mosquitto? [Д/н]: " "д")
    case "$ans" in
      [нНnN]*) say "Пропускаем. Установите брокер сами и укажите его адрес в настройках шлюза."; return 0 ;;
    esac
    install_mosquitto || { say "Не удалось определить пакетный менеджер — установите mosquitto вручную."; return 0; }
  fi

  # Уже настроенный брокер не трогаем: перезаписать чужой конфиг — верный
  # способ уронить работающий объект.
  CONF="/etc/mosquitto/conf.d/mqtt2mimismart.conf"
  if [ -f "$CONF" ]; then
    say "Настройка брокера уже есть ($CONF), не трогаем."
    return 0
  fi
  if ls /etc/mosquitto/conf.d/*.conf >/dev/null 2>&1; then
    say "В /etc/mosquitto/conf.d уже есть свои настройки — брокер не трогаем."
    return 0
  fi

  # --- с паролями или без ---
  if [ -z "$MQTT_MODE" ]; then
    say ""
    say "Как настроить доступ к брокеру?"
    say "  1 — с паролями (рекомендую): отдельные учётные записи для устройств"
    say "      и для шлюза. Нужно, если в сети бывают чужие устройства или гости."
    say "  2 — без паролей: подключиться сможет любой, кто в сети. По этой шине"
    say "      ходят команды, щёлкающие нагрузку на 230 В, — годится только для"
    say "      изолированной сети."
    ans=$(ask "Ваш выбор [1]: " "1")
    case "$ans" in
      2) MQTT_MODE="anon" ;;
      *) MQTT_MODE="auth" ;;
    esac
  fi

  if [ "$MQTT_MODE" = "auth" ]; then
    MQTT_DEVICES_PASS="${DEVICES_PASSWORD:-}"
    MQTT_GATEWAY_PASS="${GATEWAY_PASSWORD:-}"

    [ -n "$MQTT_DEVICES_PASS" ] || MQTT_DEVICES_PASS=$(ask_secret "Пароль для устройств: ")
    [ -n "$MQTT_GATEWAY_PASS" ] || MQTT_GATEWAY_PASS=$(ask_secret "Пароль для нашего шлюза: ")

    if [ -z "$MQTT_DEVICES_PASS" ] || [ -z "$MQTT_GATEWAY_PASS" ]; then
      say "Пароли не заданы — настраиваем брокер без них."
      MQTT_MODE="anon"
    fi
  fi

  mkdir -p /etc/mosquitto/conf.d

  if [ "$MQTT_MODE" = "auth" ]; then
    PASSWD_FILE="/etc/mosquitto/passwd"
    # -b задаёт пароль сразу, без интерактивного запроса; -c создаёт файл и
    # затирает существующий, поэтому только когда файла ещё нет.
    if [ -f "$PASSWD_FILE" ]; then
      mosquitto_passwd -b "$PASSWD_FILE" "$MQTT_USER_DEVICES" "$MQTT_DEVICES_PASS"
    else
      mosquitto_passwd -c -b "$PASSWD_FILE" "$MQTT_USER_DEVICES" "$MQTT_DEVICES_PASS"
    fi
    mosquitto_passwd -b "$PASSWD_FILE" "$MQTT_USER_GATEWAY" "$MQTT_GATEWAY_PASS"

    # Без файла паролей брокер с allow_anonymous false не пустит вообще никого,
    # и объект встанет молча. Лучше остаться без учётных записей, чем без связи.
    if [ ! -s "$PASSWD_FILE" ]; then
      say "Не удалось создать файл паролей — настраиваем брокер без учётных записей."
      MQTT_MODE="anon"
    else
      chown root:mosquitto "$PASSWD_FILE" 2>/dev/null || true
      chmod 0640 "$PASSWD_FILE" 2>/dev/null || true
    fi
  fi

  if [ "$MQTT_MODE" = "auth" ]; then

    cat > "$CONF" <<CONFEOF
# Настройка от install.sh шлюза mqtt2mimismart.
#
# listener без адреса означает «слушать на всех интерфейсах» — именно это и
# нужно, чтобы устройства достучались. С версии 2.0 mosquitto по умолчанию
# слушает только localhost, и устройства при этом молча не подключаются.
listener 1883

# Обе строки имеют смысл только вместе: password_file при allow_anonymous true
# ничего не защищает, и кажется, что защита работает, хотя её нет.
allow_anonymous false
password_file /etc/mosquitto/passwd

persistence true
persistence_location /var/lib/mosquitto/
log_dest file /var/log/mosquitto/mosquitto.log
CONFEOF
  else
    cat > "$CONF" <<'CONFEOF'
# Настройка от install.sh шлюза mqtt2mimismart.
#
# Без учётных записей: подключиться может любой, кто в сети. Годится для
# изолированной сети; если сеть общая с жильцами или гостями, заведите пароли —
# по этой шине ходят команды, щёлкающие нагрузку на 230 В.
listener 1883
allow_anonymous true

persistence true
persistence_location /var/lib/mosquitto/
log_dest file /var/log/mosquitto/mosquitto.log
CONFEOF
  fi

  systemctl enable mosquitto >/dev/null 2>&1 || true
  systemctl restart mosquitto 2>/dev/null || service mosquitto restart 2>/dev/null || true
  sleep 1

  if systemctl is-active --quiet mosquitto 2>/dev/null; then
    say "Брокер настроен и запущен."
  else
    say "Предупреждение: брокер не поднялся. Смотрите: journalctl -u mosquitto -n 30"
  fi
}

# Осечка с брокером не должна ронять установку шлюза: шлюз поднимется и честно
# покажет в вебе, что связи нет, а брокер можно доставить отдельно. Вызов через
# "||" заодно отключает set -e внутри функции — иначе первая же неудачная
# команда обрывала бы весь скрипт на середине.
setup_mosquitto || say "Предупреждение: настроить брокер не удалось, ставим шлюз без него."

# --- пользователь и каталоги ----------------------------------------------
if ! id mqtt2mimismart >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin mqtt2mimismart 2>/dev/null \
    || adduser --system --no-create-home --shell /usr/sbin/nologin mqtt2mimismart 2>/dev/null \
    || say "предупреждение: не удалось завести пользователя, служба пойдёт от root"
fi

mkdir -p "$BIN_DIR" "$STATE_DIR"
chown mqtt2mimismart:mqtt2mimismart "$STATE_DIR" 2>/dev/null || true
chmod 0700 "$STATE_DIR"

# --- ставим ----------------------------------------------------------------
UPDATE=no
[ -f "$BIN_DIR/mqtt2mimismart" ] && UPDATE=yes

# Старый бинарник сохраняем: откат должен быть в одно движение.
if [ "$UPDATE" = yes ]; then
  cp -f "$BIN_DIR/mqtt2mimismart" "$BIN_DIR/mqtt2mimismart.old" 2>/dev/null || true
fi

# Замена через переименование в том же каталоге: она атомарна, и служба не
# увидит наполовину записанный файл.
cp "$TMP/gateway" "$BIN_DIR/mqtt2mimismart.new"
mv -f "$BIN_DIR/mqtt2mimismart.new" "$BIN_DIR/mqtt2mimismart"

# --- служба ----------------------------------------------------------------
if [ ! -f "/etc/systemd/system/$SERVICE.service" ]; then
  say "Заводим службу systemd…"
  cat > "/etc/systemd/system/$SERVICE.service" <<UNIT
[Unit]
Description=Шлюз MQTT — MimiSmart
Documentation=https://github.com/$REPO
After=network-online.target mosquitto.service
Wants=network-online.target

[Service]
Type=simple
User=mqtt2mimismart
Group=mqtt2mimismart
ExecStart=$BIN_DIR/mqtt2mimismart --addr $ADDR --db $STATE_DIR/gateway.db --log-level info
Restart=always
RestartSec=5s

StandardOutput=journal
StandardError=journal

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=$STATE_DIR
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
# Именно read-only: logic.xml лежит в домашнем каталоге сервера умного дома,
# и шлюзу нужно его читать.
ProtectHome=read-only

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable "$SERVICE"
else
  systemctl daemon-reload
fi

systemctl restart "$SERVICE"
sleep 2

if systemctl is-active --quiet "$SERVICE"; then
  if [ "$UPDATE" = yes ]; then
    say "Обновлено до $VERSION, служба перезапущена."
    say "Журнал: journalctl -u $SERVICE -f"
  else
    IP=$(hostname -I 2>/dev/null | awk '{print $1}')
    [ -n "$IP" ] || IP="адрес-сервера"
    say ""
    say "Установлено. Откройте http://$IP:${ADDR##*:} и заполните настройки."
    say ""
    say "Что вводить в разделе «Настройки»:"
    if [ "$MQTT_MODE" = "auth" ] && [ -n "$MQTT_GATEWAY_PASS" ]; then
      say "  Брокер:      127.0.0.1:1883"
      say "  Пользователь: $MQTT_USER_GATEWAY"
      say "  Пароль:       тот, что вы задали для шлюза"
      say ""
      say "А в настройках каждого устройства Shelly:"
      say "  Server:       $IP:1883"
      say "  Username:     $MQTT_USER_DEVICES"
      say "  Password:     тот, что вы задали для устройств"
    else
      say "  Брокер:      127.0.0.1:1883"
      say "  Пользователь и пароль оставьте пустыми — брокер без учётных записей."
      say ""
      say "А в настройках каждого устройства Shelly укажите Server: $IP:1883"
    fi
    say ""
    say "Ключ сервера статистики и его адрес возьмите из настроек умного дома."
    say "Журнал: journalctl -u $SERVICE -f"
  fi
else
  say "Служба не поднялась. Смотрите: journalctl -u $SERVICE -n 50"
  if [ -f "$BIN_DIR/mqtt2mimismart.old" ]; then
    say "Откат: mv $BIN_DIR/mqtt2mimismart.old $BIN_DIR/mqtt2mimismart && systemctl restart $SERVICE"
  fi
  exit 1
fi
