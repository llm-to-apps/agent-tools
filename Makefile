.PHONY: build test run

build:
	go build -o bin/agent-tools ./cmd/agent-tools

test:
	go test ./...

run:
	go run ./cmd/agent-tools
