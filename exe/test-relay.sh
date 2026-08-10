#!/usr/bin/env bash
# Поэтапная проверка связки «реле Shelly 2.5 → бинарник → умный дом».
#
# Этапы 1-5 только читают состояние и безопасны.
# Этапы 6-7 реально щёлкают нагрузкой и включаются флагом --switch.
#
#   ./test-relay.sh                       # проверки только на чтение
#   ./test-relay.sh --switch              # плюс включение/выключение канала 0
#   ./test-relay.sh --ip 192.168.20.222   # другой адрес реле
#   ./test-relay.sh --channel 1 --switch  # щёлкать канал 1
#
# Логин и пароль реле берутся из 94.yaml, либо из переменных окружения
# SHELLY_USER и SHELLY_PASSWORD, либо из флагов --user / --password.
# Они нужны только для curl-проверок: сам бинарник читает их из конфига.

set -uo pipefail

HELP_LINES='2,15p'

IP="192.168.20.222"
CHANNEL=0
DO_SWITCH=0
USERNAME="${SHELLY_USER:-}"
PASSWORD="${SHELLY_PASSWORD:-}"

while [[ $# -gt 0 ]]; do
	case "$1" in
	--ip) IP="$2"; shift 2 ;;
	--channel) CHANNEL="$2"; shift 2 ;;
	--user) USERNAME="$2"; shift 2 ;;
	--password) PASSWORD="$2"; shift 2 ;;
	--switch) DO_SWITCH=1; shift ;;
	-h | --help) sed -n "$HELP_LINES" "$0"; exit 0 ;;
	*) echo "неизвестный аргумент: $1" >&2; exit 2 ;;
	esac
done

cd "$(dirname "$0")" || exit 1

# Имя бинарника совпадает с Makefile: собираем туда же, где лежит 94.yaml,
# потому что конфиг ищется рядом с исполняемым файлом.
BIN="./${BINARY:-93.sh}"
PKG="./cmd/shelly-2.5"
CURL=(curl -fsS --max-time 5)

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
fail() { printf '\033[31mПРОВАЛ:\033[0m %s\n' "$*" >&2; exit 1; }
ok() { printf '\033[32mОК:\033[0m %s\n' "$*"; }

# hex кодирует строку команды так, как её передаёт умный дом.
hex() { printf '%s' "$1" | od -An -tx1 | tr -d ' \n'; }

# run вызывает бинарник с HEX-командой. Позиция аргумента определяется
# автоматически, поэтому передаём его единственным.
run() {
	local payload="$1"
	local encoded
	encoded="$(hex "$payload")"
	echo "  команда: $payload"
	echo "  hex:     $encoded"
	SHELLY_DRY_RUN=1 "$BIN" "$encoded"
}

# Учётные данные подтягиваем из конфига, если не заданы явно.
if [[ -z "$USERNAME" && -f 94.yaml ]]; then
	USERNAME="$(sed -n 's/^[[:space:]]*user:[[:space:]]*"\{0,1\}\([^"#]*\)"\{0,1\}.*/\1/p' 94.yaml | head -1 | tr -d ' ')"
	PASSWORD="$(sed -n 's/^[[:space:]]*password:[[:space:]]*"\{0,1\}\([^"#]*\)"\{0,1\}.*/\1/p' 94.yaml | head -1 | tr -d ' ')"
fi
if [[ -n "$USERNAME" ]]; then
	CURL+=(--user "$USERNAME:$PASSWORD")
fi

step "1. Реле отвечает и это действительно Shelly 2.5"
INFO="$("${CURL[@]}" "http://$IP/shelly")" || fail "реле $IP не отвечает на /shelly"
echo "$INFO"
case "$INFO" in
*SHSW-25*) ok "модель SHSW-25" ;;
*) echo "ВНИМАНИЕ: в ответе нет SHSW-25 — это другая модель?" ;;
esac
case "$INFO" in
*'"auth":true'*)
	if [[ -z "$USERNAME" ]]; then
		fail "на реле включён пароль, но учётные данные не найдены — задайте user/password в shelly.yaml или флагами"
	fi
	ok "на реле включён пароль, учётные данные подставлены"
	;;
esac

step "2. Режим устройства: нужен relay, а не roller"
MODE="$("${CURL[@]}" "http://$IP/settings" | tr ',' '\n' | grep -m1 '"mode"')" ||
	fail "не удалось прочитать /settings (проверьте логин и пароль)"
echo "$MODE"
case "$MODE" in
*relay*) ok "режим relay" ;;
*) fail "устройство в режиме roller — переключите: curl \"http://$IP/settings?mode=relay\"" ;;
esac

step "3. Текущее состояние обоих каналов"
"${CURL[@]}" "http://$IP/status" | tr ',' '\n' | grep -E '"ison"|"power"|"total"|"overpower"|"tC"' || true

step "4. Сборка бинарника"
go build -o "$BIN" "$PKG" || fail "сборка не прошла"
ok "собран $BIN из $PKG"
[[ -f 94.yaml ]] || fail "рядом с бинарником нет shelly.yaml — конфиг ищется именно там"

step "5. Сухой прогон: опрос статуса через бинарник"
run "$IP|status|" || fail "бинарник вернул ошибку на status"
ok "статус прочитан и разложен по точкам умного дома"

if [[ "$DO_SWITCH" -eq 0 ]]; then
	printf '\n\033[33mПереключение пропущено.\033[0m Запустите с --switch, чтобы проверить on/off.\n'
	printf 'Учтите: канал %s реально щёлкнет подключённой нагрузкой.\n' "$CHANNEL"
	exit 0
fi

step "6. Включение канала $CHANNEL"
run "$IP|on|$CHANNEL" || fail "не удалось включить канал $CHANNEL"
sleep 2
"${CURL[@]}" "http://$IP/relay/$CHANNEL" | tr ',' '\n' | grep -E '"ison"|"source"' || true

step "7. Выключение канала $CHANNEL"
run "$IP|off|$CHANNEL" || fail "не удалось выключить канал $CHANNEL"
sleep 2
"${CURL[@]}" "http://$IP/relay/$CHANNEL" | tr ',' '\n' | grep -E '"ison"|"source"' || true

printf '\n\033[32mВсе этапы пройдены.\033[0m Дальше: пропишите реальные id/subid в 94.yaml,\n'
printf 'уберите SHELLY_DRY_RUN и проверьте, что значения доходят до умного дома.\n'
