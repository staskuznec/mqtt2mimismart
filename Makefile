GATEWAY = mqtt2mimismart
LEGACY  = 94.sh

# Версия попадает в бинарник: её отдаёт флаг -version и проверка /healthz.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)

# CGO не нужен: SQLite взят чистый на Go, поэтому сборка под ARMv7 ничем не
# отличается от сборки под хост.
ARM = GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0

.PHONY: build build-armv7 legacy legacy-armv7 test fmt clean

# Демон-шлюз.
build:
	go build -ldflags "$(LDFLAGS)" -o ./exe/$(GATEWAY) ./cmd/gateway

build-armv7:
	$(ARM) go build -ldflags "$(LDFLAGS)" -o ./exe/$(GATEWAY)-armv7 ./cmd/gateway

# Старый бинарник реле по HTTP. Уходит вместе с переводом объекта на MQTT,
# до тех пор собирается для отката.
legacy:
	go build -o ./exe/$(LEGACY) ./cmd/shelly-2.5

legacy-armv7:
	$(ARM) go build -o ./exe/$(LEGACY) ./cmd/shelly-2.5

test: fmt
	go vet ./...
	go test -race ./...

# gofmt -l сам по себе завершается успешно, даже когда что-то нашёл, поэтому
# проверяем, что вывод пустой: иначе неотформатированный код молча проезжает.
fmt:
	@test -z "$$(gofmt -l . | grep -v '^docs/')" || \
		{ echo "не отформатировано:"; gofmt -l . | grep -v '^docs/'; exit 1; }

clean:
	rm -f ./exe/$(GATEWAY) ./exe/$(GATEWAY)-armv7 ./exe/$(LEGACY)