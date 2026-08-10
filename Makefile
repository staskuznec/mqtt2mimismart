BINARY=94.sh


build-armv7:
	GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -o $(BINARY) ./cmd/shelly-2.5

build:
	go build -o $(BINARY) ./cmd/shelly-2.5

clean:
	rm -f $(BINARY)