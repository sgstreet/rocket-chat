BINARY  := rocket-chat
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build test lint fmt vet clean

all: lint test build

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/rocket-chat

test:
	go test -race ./...

vet:
	go vet ./...

lint: vet
	golangci-lint run

fmt:
	gofmt -w .

clean:
	rm -rf bin
