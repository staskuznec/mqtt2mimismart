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
  else
    say "Установлено. Откройте http://$(hostname -I 2>/dev/null | awk '{print $1}'):${ADDR##*:} и заполните настройки."
  fi
  say "Журнал: journalctl -u $SERVICE -f"
else
  say "Служба не поднялась. Смотрите: journalctl -u $SERVICE -n 50"
  if [ -f "$BIN_DIR/mqtt2mimismart.old" ]; then
    say "Откат: mv $BIN_DIR/mqtt2mimismart.old $BIN_DIR/mqtt2mimismart && systemctl restart $SERVICE"
  fi
  exit 1
fi
