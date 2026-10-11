VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
GO_LICENSES := go run github.com/google/go-licenses/v2@v2.0.1

.DEFAULT_GOAL := build
.PHONY: build test install redeploy check lint fmt vet licenses

build:
	go build -ldflags "$(LDFLAGS)" -o bin/sock ./cmd/sock

test:
	go test -race ./...

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/sock

redeploy:
	@echo "sock-cli has nothing to deploy; run 'make install' to update the local binary"

lint:
	golangci-lint run

fmt:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

licenses:
	$(GO_LICENSES) check ./... --disallowed_types=forbidden,restricted

check: fmt vet lint licenses test
