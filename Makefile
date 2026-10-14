VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
GO_LICENSES := go run github.com/google/go-licenses/v2@v2.0.1

.DEFAULT_GOAL := build
.PHONY: build test install redeploy check check-head lint fmt vet tidy licenses ci-pins pins

# Pins (ADR 1): every go target builds against the sock-contract and
# sock-client tags in .contract-ref and .client-ref, the tags CI checks out,
# not whatever the sibling checkouts have at HEAD. make pins clones each tag
# once under PIN_CACHE, from the sibling checkout (fetching the tag when it
# lacks it), then reuses it offline, and writes .pins/go.work, whose replace
# lines override go.mod's ../contract and ../client with those clones.
# go mod tidy ignores go.work, so tidy reads .pins/go.mod, a copy of go.mod
# with the same replaces. go.mod itself is never edited.
# make check-head (PINS empty) builds against the siblings' HEAD instead.
PIN_CACHE ?= $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/sock-pins
PINNED := contract client
PINS ?= $(PINNED)
ifneq ($(PINS),)
export GOWORK := $(CURDIR)/.pins/go.work
TIDY_FLAGS := -modfile=$(CURDIR)/.pins/go.mod
endif

# Never touches the sibling checkout's HEAD, index or working tree: a tag the
# sibling lacks is fetched as a ref, and each clone is made in a temp dir and
# renamed into place, so concurrent runs share one clone and never see half
# of one.
pins:
ifneq ($(PINS),)
	@set -e; mkdir -p .pins; edits=; work=.pins/go.work.$$$$; \
	printf 'go %s\n\nuse ..\n\n' "$$(awk '$$1 == "go" {print $$2}' go.mod)" > $$work; \
	for m in $(PINS); do \
		tag=$$(cat .$$m-ref); dir="$(PIN_CACHE)/$$m/$$tag"; \
		if [ ! -d "$$dir" ]; then \
			git -C ../$$m rev-parse -q --verify "refs/tags/$$tag" >/dev/null || git -C ../$$m fetch -q origin tag "$$tag"; \
			mkdir -p "$(PIN_CACHE)/$$m"; tmp=$$(mktemp -d "$(PIN_CACHE)/$$m/.tmp.XXXXXX"); \
			git clone -q --no-checkout ../$$m "$$tmp"; \
			git -C "$$tmp" -c advice.detachedHead=false checkout -q "$$tag^{commit}"; \
			perl -e 'rename($$ARGV[0], $$ARGV[1]) or -d $$ARGV[1] or die "$$ARGV[1]: $$!\n"' "$$tmp" "$$dir"; \
			rm -rf "$$tmp"; \
		fi; \
		echo "replace github.com/guygrigsby/sock-$$m => $$dir" >> $$work; \
		edits="$$edits -replace=github.com/guygrigsby/sock-$$m=$$dir"; \
	done; \
	GOWORK=off go mod edit $$edits -print > .pins/go.mod.$$$$; \
	cp go.sum .pins/go.sum.$$$$; \
	mv .pins/go.sum.$$$$ .pins/go.sum; mv .pins/go.mod.$$$$ .pins/go.mod; mv $$work .pins/go.work
endif

build test install vet lint licenses tidy: pins

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
	GOWORK=off go mod tidy -diff $(TIDY_FLAGS)

# ci-pins fails unless every sibling checkout in ci.yml takes its ref from the
# ref file make check builds against, so CI and make check cannot pin
# different tags.
ci-pins:
	@for m in $(PINNED); do \
		grep -q "$$m=\$$(cat [^)]*\.$$m-ref)\" >> \"\$$GITHUB_OUTPUT\"" .github/workflows/ci.yml && \
		grep -q "ref: \$${{ steps.pins.outputs.$$m }}" .github/workflows/ci.yml || \
		{ echo "ci.yml must check out sock-$$m at the tag in .$$m-ref (steps.pins.outputs.$$m)" >&2; exit 1; }; \
	done
	@! grep -Eq '^ *ref: [^$$]' .github/workflows/ci.yml || { echo 'ci.yml hardcodes a ref; read it from a ref file' >&2; exit 1; }

check: ci-pins tidy fmt vet lint licenses test

check-head:
	$(MAKE) check PINS=
