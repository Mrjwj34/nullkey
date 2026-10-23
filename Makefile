SHELL := /bin/bash
GO ?= go

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/Mrjwj34/nullkey/internal/version.Version=$(VERSION) \
           -X github.com/Mrjwj34/nullkey/internal/version.Commit=$(COMMIT) \
           -X github.com/Mrjwj34/nullkey/internal/version.Date=$(DATE)

BIN := bin/nullkeyd

.PHONY: all build run test test-race fmt fmt-check vet tidy lint check contract \
        acceptance acceptance-ci acceptance-compile release-compile release-product \
        release-slo release-check clean

all: check

build:
	@mkdir -p bin
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/nullkeyd

run:
	$(GO) run ./cmd/nullkeyd

test:
	$(GO) test -count=1 ./...

test-race:
	$(GO) test -race -count=1 ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

lint: fmt-check vet

contract:
	$(GO) test -count=1 -v ./internal/contract

check: lint test build contract acceptance-compile

acceptance:
	NULLKEY_ACCEPTANCE_STRICT=1 $(GO) test -tags acceptance -count=1 -timeout 15m ./test/acceptance

acceptance-ci:
	$(GO) test -tags acceptance -count=1 -timeout 15m ./test/acceptance

acceptance-compile:
	$(GO) test -tags acceptance -run '^$$' ./test/acceptance

release-compile:
	$(GO) test -tags 'acceptance release' -run '^$$' ./test/acceptance

release-product:
	$(GO) test -tags 'acceptance release' -count=1 -run '^TestProduct_' -v ./test/acceptance

release-slo:
	NULLKEY_RELEASE_STRICT=1 ./scripts/slo.sh

release-check: release-product release-slo

clean:
	rm -rf bin coverage.out
