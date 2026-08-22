.PHONY: build test run voice text

build:
	mkdir -p bin
	go build -o bin/homevoice-gateway ./cmd/gateway
	go build -o bin/homevoice-satellite ./cmd/satellite

test:
	go test ./...
	go vet ./...

run:
	go run ./cmd/gateway

voice:
	go run ./cmd/satellite -mode voice

text:
	go run ./cmd/satellite -mode text
