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

# Профили устройств лежат отдельно, в доступном месте: туда кладут свои файлы,
# и рыться ради этого в /var/lib не надо.
PROFILES_DIR="${PROFILES_DIR:-/home/sh2/mqtt/profiles}"
SERVICE="mqtt2mimismart"
ADDR="${ADDR:-}"

# Корень веб-сервера, где лежит панель умного дома, и подкаталог, в котором
# рядом с ней встанет шлюз. Панель обычно в <корень>/MimiSetup — по ней корень
# и опознаётся.
WEB_ROOT="${WEB_ROOT:-}"
BASE_PATH="${BASE_PATH:-/mqtt}"

find_web_root() {
  [ -z "$WEB_ROOT" ] || { printf '%s' "$WEB_ROOT"; return 0; }
  for d in /home/html /var/www/html /home/sh2/web /var/www; do
    [ -d "$d/MimiSetup" ] && { printf '%s' "$d"; return 0; }
  done
  printf ''
}
WEB_ROOT=$(find_web_root)

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

# Файл вкладки для панели: не критичен, поэтому без сверки и без отказа.
curl -fsSL "$BASE/mimisetup-mqtt-tab.js" -o "$TMP/mqtt-tab.js" 2>/dev/null || true

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
MQTT_ALREADY=no            # брокер был настроен до нас

# has_listener сообщает, что порт уже кем-то объявлен.
has_listener() {
  grep -qs '^[[:space:]]*listener' /etc/mosquitto/mosquitto.conf 2>/dev/null && return 0
  for f in /etc/mosquitto/conf.d/*.conf; do
    [ -f "$f" ] || continue
    [ "$f" = "/etc/mosquitto/conf.d/mqtt2mimismart.conf" ] && continue
    grep -qs '^[[:space:]]*listener' "$f" && return 0
  done
  return 1
}

# broker_ready сообщает, что брокер уже настроен и работать с ним можно.
broker_ready() {
  # Слушает порт — значит настроен и запущен. Самый надёжный признак: он не
  # зависит от того, где именно лежит конфиг.
  if command -v ss >/dev/null 2>&1; then
    ss -tln 2>/dev/null | grep -q "[:.]1883 " && return 0
  elif command -v netstat >/dev/null 2>&1; then
    netstat -tln 2>/dev/null | grep -q "[:.]1883 " && return 0
  fi

  # Не запущен, но настроен: listener есть в главном конфиге или в conf.d.
  grep -qs '^[[:space:]]*listener' /etc/mosquitto/mosquitto.conf 2>/dev/null && return 0
  for f in /etc/mosquitto/conf.d/*.conf; do
    [ -f "$f" ] || continue
    grep -qs '^[[:space:]]*listener' "$f" && return 0
  done
  return 1
}

# detect_broker_mode определяет, с паролями настроен брокер или без: от этого
# зависит, что печатать в конце — какие учётные данные вводить в шлюзе.
detect_broker_mode() {
  MQTT_MODE="anon"
  for f in /etc/mosquitto/mosquitto.conf /etc/mosquitto/conf.d/*.conf; do
    [ -f "$f" ] || continue
    if grep -qs '^[[:space:]]*password_file' "$f" ||
       grep -qs '^[[:space:]]*allow_anonymous[[:space:]]\+false' "$f"; then
      MQTT_MODE="auth"
      return 0
    fi
  done
}

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
  #
  # Смотрим по факту, а не по одному каталогу: настройка может лежать и в
  # главном mosquitto.conf, и в conf.d, а самый надёжный признак — брокер уже
  # слушает свой порт.
  if broker_ready; then
    detect_broker_mode
    say ""
    if [ "$MQTT_MODE" = "auth" ]; then
      say "Брокер уже настроен, с учётными записями."
    else
      say "Брокер уже настроен, без учётных записей: подключиться может любой,"
      say "кто в сети. По этой шине ходят команды, щёлкающие нагрузку на 230 В."
    fi
    ans=$(ask "Оставить его настройки как есть? [Д/н]: " "д")
    case "$ans" in
      [нНnN]*)
        say "Перенастраиваем."
        MQTT_MODE="" # спросим заново, как настраивать
        ;;
      *)
        MQTT_ALREADY=yes
        return 0
        ;;
    esac
  fi

  CONF="/etc/mosquitto/conf.d/mqtt2mimismart.conf"

  # Настройки, дописанные в главный конфиг, наш файл в conf.d не отменяет:
  # mosquitto читает оба, и последнее значение побеждает. Об этом стоит знать,
  # если что-то настроено там и продолжает действовать.

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

  # Свой listener добавляем, только если его ещё нет: два на одном порту — это
  # «Address already in use», и брокер не поднимется вовсе.
  if has_listener; then
    LISTENER_LINE="# listener не добавляем: он уже задан в другом конфиге"
  else
    LISTENER_LINE="listener 1883"
  fi

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
$LISTENER_LINE

# Обе строки имеют смысл только вместе: password_file при allow_anonymous true
# ничего не защищает, и кажется, что защита работает, хотя её нет.
allow_anonymous false
password_file /etc/mosquitto/passwd
CONFEOF
  else
    cat > "$CONF" <<CONFEOF
# Настройка от install.sh шлюза mqtt2mimismart.
#
# Без учётных записей: подключиться может любой, кто в сети. Годится для
# изолированной сети; если сеть общая с жильцами или гостями, заведите пароли —
# по этой шине ходят команды, щёлкающие нагрузку на 230 В.
$LISTENER_LINE
allow_anonymous true
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

# --- веб-сервер: шлюз рядом с панелью умного дома --------------------------
#
# На сервере статистики уже стоит веб-сервер с панелью MimiSetup. Логично
# открывать шлюз оттуда же — http://сервер/mqtt/, — а не отдельным портом:
# один адрес, одна точка входа, и порт 8080 не надо помнить и открывать.
PROXY=no

setup_proxy() {
  if [ "${SKIP_PROXY:-}" = "1" ]; then
    return 0
  fi

  WEBSRV=""
  if command -v apache2ctl >/dev/null 2>&1 || command -v apachectl >/dev/null 2>&1; then
    WEBSRV="apache"
  elif command -v nginx >/dev/null 2>&1; then
    WEBSRV="nginx"
  else
    say "Веб-сервер не найден — шлюз будет доступен отдельным портом."
    return 0
  fi

  say ""
  say "Найден веб-сервер: $WEBSRV."
  say "Шлюз можно открыть рядом с панелью умного дома, по адресу"
  say "http://сервер$BASE_PATH/ — вместо отдельного порта 8080."
  ans=$(ask "Настроить? [Д/н]: " "д")
  case "$ans" in
    [нНnN]*) return 0 ;;
  esac

  if [ "$WEBSRV" = "apache" ]; then
    # conf-available — штатный механизм apache: конфиг применяется ко всем
    # виртуальным хостам, и чужие файлы при этом не правятся.
    a2enmod proxy proxy_http >/dev/null 2>&1 || true
    cat > /etc/apache2/conf-available/mqtt2mimismart.conf <<PROXYEOF
# Шлюз mqtt2mimismart рядом с панелью умного дома.
# Поставлено install.sh; удалить: a2disconf mqtt2mimismart
<Location $BASE_PATH/>
    ProxyPass        http://127.0.0.1:8080$BASE_PATH/
    ProxyPassReverse http://127.0.0.1:8080$BASE_PATH/
</Location>
PROXYEOF
    a2enconf mqtt2mimismart >/dev/null 2>&1 || true
    if apache2ctl configtest >/dev/null 2>&1 || apachectl configtest >/dev/null 2>&1; then
      systemctl reload apache2 2>/dev/null || service apache2 reload 2>/dev/null || true
      PROXY=yes
      say "Apache настроен."
    else
      say "Предупреждение: apache не принял конфиг, оставляем отдельный порт."
      a2disconf mqtt2mimismart >/dev/null 2>&1 || true
    fi
    return 0
  fi

  # nginx: вставлять location в чужой server-блок автоматически нельзя —
  # так проще всего уронить работающую панель. Кладём готовый кусок и
  # объясняем, куда его подключить.
  mkdir -p /etc/nginx/snippets
  cat > /etc/nginx/snippets/mqtt2mimismart.conf <<PROXYEOF
# Шлюз mqtt2mimismart рядом с панелью умного дома.
location $BASE_PATH/ {
    proxy_pass http://127.0.0.1:8080$BASE_PATH/;
    proxy_set_header Host \$host;
    proxy_set_header X-Real-IP \$remote_addr;
}
PROXYEOF
  say ""
  say "Для nginx готов кусок конфига: /etc/nginx/snippets/mqtt2mimismart.conf"
  say "Добавьте в нужный server-блок строку:"
  say "    include snippets/mqtt2mimismart.conf;"
  say "и выполните: nginx -t && systemctl reload nginx"
  say "Автоматически не вставляем — так проще всего уронить работающую панель."
  say ""
  ans=$(ask "Шлюз уже подключён к nginx этим сниппетом? [д/Н]: " "н")
  case "$ans" in
    [дДyY]*) PROXY=yes ;;
  esac
}

# add_web_link кладёт в корень панели страничку-переход.
#
# Пункт в само меню панели не добавить: она собрана в один минифицированный
# файл, и правка пережила бы ровно до её обновления. Зато отдельная страница
# рядом с ней открывается по понятному адресу и ничего не ломает.
add_web_link() {
  [ -n "$WEB_ROOT" ] && [ -d "$WEB_ROOT" ] || return 0
  [ "$PROXY" = yes ] || return 0

  cat > "$WEB_ROOT/mqtt.html" <<LINKEOF
<!doctype html>
<meta charset="utf-8">
<title>Шлюз MQTT</title>
<meta http-equiv="refresh" content="0; url=$BASE_PATH/">
<p>Переход к шлюзу MQTT: <a href="$BASE_PATH/">$BASE_PATH/</a></p>
LINKEOF
  say "Ссылка на шлюз: http://сервер/mqtt.html (или сразу $BASE_PATH/)"

}

# add_panel_tab добавляет вкладку «MQTT» в верхнее меню панели MimiSetup.
#
# Сам app.js не трогаем: он собран Sencha Cmd в один минифицированный файл, и
# правка в нём не переживёт обновления панели, а найти её потом невозможно.
# Вместо этого кладём рядом свой файл и подключаем его строкой в index.php —
# читаемой, восстановимой и заметной.
add_panel_tab() {
  PANEL="$WEB_ROOT/MimiSetup"
  [ -d "$PANEL" ] && [ -f "$PANEL/index.php" ] || return 0

  ans=$(ask "Добавить вкладку «MQTT» в меню панели MimiSetup? [Д/н]: " "д")
  case "$ans" in
    [нНnN]*) return 0 ;;
  esac

  # Файл вкладки всегда перезаписываем: он наш, и в нём мог поменяться адрес.
  if [ -f "$TMP/mqtt-tab.js" ]; then
    cp "$TMP/mqtt-tab.js" "$PANEL/mqtt-tab.js"
  else
    curl -fsSL "$BASE/mimisetup-mqtt-tab.js" -o "$PANEL/mqtt-tab.js" 2>/dev/null || {
      say "Не удалось получить файл вкладки — пропускаем."
      return 0
    }
  fi
  # Адрес шлюза во вкладке. За прокси это подкаталог того же сервера, без
  # прокси — прямой порт: вкладка полезна в обоих случаях, просто во втором
  # адрес абсолютный.
  if [ "$PROXY" = yes ]; then
    TAB_URL="$BASE_PATH/"
  else
    TAB_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
    [ -n "$TAB_IP" ] || TAB_IP="127.0.0.1"
    TAB_URL="http://$TAB_IP:${ADDR##*:}/"
  fi

  # Правим через временный файл: у sed -i разный синтаксис в GNU и BSD, и
  # полагаться на конкретный означает однажды получить пустой файл.
  sed "s#var GATEWAY_URL = \"/mqtt/\";#var GATEWAY_URL = \"$TAB_URL\";#" \
    "$PANEL/mqtt-tab.js" > "$PANEL/mqtt-tab.js.tmp" &&
    mv -f "$PANEL/mqtt-tab.js.tmp" "$PANEL/mqtt-tab.js"

  # Файл вкладки должен обновляться кнопкой в вебе, а шлюз работает не от root:
  # без права на запись он каждый раз просил бы лезть в консоль.
  chown mqtt2mimismart "$PANEL/mqtt-tab.js" 2>/dev/null || true
  chmod 0644 "$PANEL/mqtt-tab.js" 2>/dev/null || true

  if grep -q "mqtt-tab.js" "$PANEL/index.php" 2>/dev/null; then
    say "Вкладка «MQTT» в панели уже подключена."
    return 0
  fi

  # Резервная копия перед правкой чужого файла: восстановить должно быть проще,
  # чем разбираться, что сломалось.
  cp "$PANEL/index.php" "$PANEL/index.php.before-mqtt" 2>/dev/null || true

  # Строка вставляется сразу после подключения app.js — к этому моменту
  # фреймворк уже есть, а панель ещё не построена. Через awk, а не sed -i:
  # у последнего разный синтаксис в GNU и BSD.
  awk '{
    print
    if ($0 ~ /src="app\.js/ && !done) {
      print "<script type=\"text/javascript\" src=\"mqtt-tab.js\"></script>"
      done = 1
    }
  }' "$PANEL/index.php" > "$PANEL/index.php.tmp" 2>/dev/null

  if [ -s "$PANEL/index.php.tmp" ] && grep -q "mqtt-tab.js" "$PANEL/index.php.tmp"; then
    mv -f "$PANEL/index.php.tmp" "$PANEL/index.php"
    say "Вкладка «MQTT» добавлена в меню панели."
    say "Обновление панели её снесёт — запустите этот скрипт повторно, и она вернётся."
  else
    rm -f "$PANEL/index.php.tmp"
    say "Не удалось дописать index.php панели. Добавьте вручную после подключения app.js:"
    say '    <script type="text/javascript" src="mqtt-tab.js"></script>'
  fi
}

setup_proxy || say "Предупреждение: настроить веб-сервер не удалось."
add_web_link || true
add_panel_tab || say "Предупреждение: вкладку в панель добавить не удалось."

# За прокси шлюзу незачем слушать наружу: снаружи к нему ходят через панель.
if [ -z "$ADDR" ]; then
  if [ "$PROXY" = yes ]; then
    ADDR="127.0.0.1:8080"
  else
    ADDR="0.0.0.0:8080"
    BASE_PATH=""
  fi
fi

# --- пользователь и каталоги ----------------------------------------------
if ! id mqtt2mimismart >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin mqtt2mimismart 2>/dev/null \
    || adduser --system --no-create-home --shell /usr/sbin/nologin mqtt2mimismart 2>/dev/null \
    || say "предупреждение: не удалось завести пользователя, служба пойдёт от root"
fi

mkdir -p "$BIN_DIR" "$STATE_DIR" "$PROFILES_DIR"
chown mqtt2mimismart:mqtt2mimismart "$STATE_DIR" 2>/dev/null || true
chmod 0700 "$STATE_DIR"

# Профили доступны на чтение всем, на запись — шлюзу: класть туда файлы должно
# быть просто, а секретов в них нет.
chown -R mqtt2mimismart "$PROFILES_DIR" 2>/dev/null || true
chmod 0755 "$PROFILES_DIR"

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

# upgrade_unit дописывает в существующий юнит то, чего в нём ещё нет.
#
# Целиком не переписываем: человек мог поправить там пути, пользователя или
# флаги, и снести это молча нельзя. Дописываем ровно недостающее и только с
# резервной копией рядом.
upgrade_unit() {
  UNIT="/etc/systemd/system/$SERVICE.service"
  [ -f "$UNIT" ] || return 0

  NEEDS=""
  grep -q -- "--profiles" "$UNIT" || NEEDS="profiles"
  if [ "$PROXY" = yes ] && ! grep -q -- "--base-path" "$UNIT"; then
    NEEDS="$NEEDS base-path"
  fi
  [ -n "$NEEDS" ] || return 0

  cp "$UNIT" "$UNIT.before-update" 2>/dev/null || true

  # Через awk, а не sed -i: у последнего разный синтаксис в GNU и BSD, а
  # испорченный юнит означает службу, которая не поднимется.
  awk -v prof="$PROFILES_DIR" -v base="$BASE_PATH" -v proxy="$PROXY" -v state="$STATE_DIR" '
    /^ExecStart=/ {
      if ($0 !~ /--profiles/) {
        sub(/--log-level/, "--profiles " prof " --log-level")
        if ($0 !~ /--profiles/) { $0 = $0 " --profiles " prof }
      }
      if (proxy == "yes" && base != "" && $0 !~ /--base-path/) {
        sub(/--log-level/, "--base-path " base " --log-level")
      }
      print; next
    }
    /^ReadWritePaths=/ {
      if ($0 !~ prof) { $0 = $0 " " prof }
      print; seen = 1; next
    }
    { print }
    END {
      if (!seen) {
        # Строки не было вовсе: без неё ProtectSystem=strict закроет запись.
        print "ReadWritePaths=" state " " prof
      }
    }
  ' "$UNIT" > "$UNIT.new" 2>/dev/null

  if [ -s "$UNIT.new" ] && grep -q "^ExecStart=" "$UNIT.new"; then
    mv -f "$UNIT.new" "$UNIT"
    say "Служба дописана: $NEEDS. Прежний юнит рядом, $UNIT.before-update"
  else
    rm -f "$UNIT.new"
    say "Не удалось дописать юнит. Добавьте в ExecStart: --profiles $PROFILES_DIR"
  fi
}

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
ExecStart=$BIN_DIR/mqtt2mimismart --addr $ADDR --db $STATE_DIR/gateway.db --profiles $PROFILES_DIR${BASE_PATH:+ --base-path $BASE_PATH} --log-level info
Restart=always
RestartSec=5s

StandardOutput=journal
StandardError=journal

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
# Каталоги, куда службе можно писать: всё остальное закрыто ProtectSystem.
ReadWritePaths=$STATE_DIR $PROFILES_DIR
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
  # Юнит уже есть — целиком не переписываем: в нём могли поправить пути или
  # флаги. Но про новые каталоги он знать обязан, иначе после обновления шлюз
  # положит профили не туда и не сможет их читать.
  upgrade_unit
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
    if [ "$PROXY" = yes ]; then
      say "Установлено. Откройте http://$IP$BASE_PATH/ и заполните настройки."
    else
      say "Установлено. Откройте http://$IP:${ADDR##*:} и заполните настройки."
    fi
    say ""
    say "Что вводить в разделе «Настройки»:"
    if [ "$MQTT_ALREADY" = yes ]; then
      say "  Брокер:      127.0.0.1:1883"
      if [ "$MQTT_MODE" = "auth" ]; then
        say "  Пользователь и пароль — те, что заведены на брокере раньше."
      else
        say "  Пользователь и пароль оставьте пустыми — брокер без учётных записей."
      fi
    elif [ "$MQTT_MODE" = "auth" ] && [ -n "$MQTT_GATEWAY_PASS" ]; then
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
    say ""
    say "Профили устройств лежат в $PROFILES_DIR — туда можно класть свои."
    say "Журнал: journalctl -u $SERVICE -f"
  fi
else
  say "Служба не поднялась. Смотрите: journalctl -u $SERVICE -n 50"
  if [ -f "$BIN_DIR/mqtt2mimismart.old" ]; then
    say "Откат: mv $BIN_DIR/mqtt2mimismart.old $BIN_DIR/mqtt2mimismart && systemctl restart $SERVICE"
  fi
  exit 1
fi
