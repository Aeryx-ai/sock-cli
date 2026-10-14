VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
GO_LICENSES := go run github.com/google/go-licenses/v2@v2.0.1

.DEFAULT_GOAL := build
.PHONY: build test install redeploy check lint fmt vet tidy licenses

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

# tidy fails when go.mod or go.sum need `go mod tidy`, as they do when a sibling
# module the replace directives point at (sock-client, sock-contract) changed
# its requirements. Run before everything else: go vet and go test fail
# with the same complaint, less clearly.
tidy:
	go mod tidy -diff

# ci-pins fails unless every sibling checkout in ci.yml takes its ref from the
# ref file make check builds against, so CI and make check cannot pin
# different tags.
ci-pins:
	@for m in contract client; do \
		grep -q "$$m=\$$(cat [^)]*\.$$m-ref)\" >> \"\$$GITHUB_OUTPUT\"" .github/workflows/ci.yml && \
		grep -q "ref: \$${{ steps.pins.outputs.$$m }}" .github/workflows/ci.yml || \
		{ echo "ci.yml must check out sock-$$m at the tag in .$$m-ref (steps.pins.outputs.$$m)" >&2; exit 1; }; \
	done
	@! grep -Eq '^ *ref: [^$$]' .github/workflows/ci.yml || { echo 'ci.yml hardcodes a ref; read it from a ref file' >&2; exit 1; }

check: ci-pins tidy fmt vet lint licenses test
