.PHONY: build test run wake voice text setup-wakeword

build:
	mkdir -p bin
	go build -o bin/homevoice-gateway ./cmd/gateway
	go build -o bin/homevoice-satellite ./cmd/satellite

test:
	go test ./...
	go vet ./...

run:
	go run ./cmd/gateway

wake:
	go run ./cmd/satellite -mode wake

voice:
	go run ./cmd/satellite -mode voice

text:
	go run ./cmd/satellite -mode text

setup-wakeword:
	bash scripts/setup-wakeword.sh
