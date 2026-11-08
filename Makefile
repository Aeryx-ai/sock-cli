VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo dev))
LDFLAGS := -X main.version=$(VERSION)
GO_LICENSES := go run github.com/google/go-licenses/v2@v2.0.1

.DEFAULT_GOAL := build
.PHONY: build test install redeploy check lint fmt vet tidy licenses e2e-selfreg dist

# go.mod pins sock-contract and sock-client by tag; the Go proxy fetches
# them. To build against local checkouts instead, use an untracked go.work
# (gitignored).

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

# tidy fails when go.mod or go.sum need `go mod tidy`. Run before everything
# else: go vet and go test fail with the same complaint, less clearly.
tidy:
	GOWORK=off go mod tidy -diff

check: tidy fmt vet lint licenses test

# Unattended self-registration test against a local Sock dev hub.
# Not part of check: it needs the hub, Docker and a sock-mcp.
# SOCK_MCP defaults to the sock-mcp on PATH.
e2e-selfreg: build
	SOCK=$(CURDIR)/bin/sock scripts/e2e-selfreg.sh

# dist cross-builds the release archives publish-release.sh uploads, one per
# platform, all at once: sock-<os>-<arch>.tar.gz (.zip on Windows) holding the
# binary and LICENSE at the archive root, plus SHA256SUMS over them. The release
# workflow runs this.
DIST_PLATFORMS := darwin-arm64 darwin-amd64 linux-arm64 linux-amd64 windows-amd64

dist:
	rm -rf bin/dist bin/dist-stage
	$(MAKE) -j$(words $(DIST_PLATFORMS)) $(addprefix dist-,$(DIST_PLATFORMS)) VERSION=$(VERSION)
	rm -rf bin/dist-stage
	cd bin/dist && shasum -a 256 sock-* > SHA256SUMS

# Inside dist-%: $* is <os>-<arch>.
dist_os = $(word 1,$(subst -, ,$*))
dist_exe = sock$(if $(filter windows,$(dist_os)),.exe)

dist-%:
	mkdir -p bin/dist bin/dist-stage/$*
	CGO_ENABLED=0 GOOS=$(dist_os) GOARCH=$(word 2,$(subst -, ,$*)) go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dist-stage/$*/$(dist_exe) ./cmd/sock
	cp LICENSE bin/dist-stage/$*/
	$(if $(filter windows,$(dist_os)),cd bin/dist-stage/$* && zip -qX ../../dist/sock-$*.zip $(dist_exe) LICENSE,COPYFILE_DISABLE=1 tar -czf bin/dist/sock-$*.tar.gz -C bin/dist-stage/$* $(dist_exe) LICENSE)
