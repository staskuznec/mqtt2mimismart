BINARY = mqtt2mimismart

# Версия попадает в бинарник: её отдаёт флаг -version и проверка /healthz.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)

# CGO не нужен: SQLite взят чистый на Go, поэтому сборка под ARMv7 ничем не
# отличается от сборки под хост.
ARM = GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0

.PHONY: build build-armv7 test fmt clean

build:
	go build -ldflags "$(LDFLAGS)" -o ./exe/$(BINARY) ./cmd/gateway

build-armv7:
	$(ARM) go build -ldflags "$(LDFLAGS)" -o ./exe/$(BINARY)-armv7 ./cmd/gateway

test: fmt
	go vet ./...
	go test -race ./...

# gofmt -l сам по себе завершается успешно, даже когда что-то нашёл, поэтому
# проверяем, что вывод пустой: иначе неотформатированный код молча проезжает.
fmt:
	@test -z "$$(gofmt -l . | grep -v '^docs/')" || \
		{ echo "не отформатировано:"; gofmt -l . | grep -v '^docs/'; exit 1; }

clean:
	rm -f ./exe/$(BINARY) ./exe/$(BINARY)-armv7