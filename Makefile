# forge build and development entry points.
# All targets are plain commands; nothing here is required to be run in a
# particular order except `make dev` once per checkout.

GO      ?= go
BIN     := bin
PKG     := ./...
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X as215520.net/forge/internal/version.Version=$(VERSION)
export CGO_ENABLED := 0
export GOFLAGS := -mod=mod
export PATH := $(HOME)/.local/go/bin:$(HOME)/go/bin:$(HOME)/.local/bin:$(CURDIR)/bin:$(PATH)

.PHONY: all dev build test integration lint fmt vet run clean tidy tools infra-lint gen check dist

all: build

## dev: install pinned developer tools (staticcheck, govulncheck) into $GOPATH/bin
dev: tools
	@echo "toolchain: $$($(GO) version)"
	@echo "git:       $$(git --version)"
	@command -v tofu >/dev/null 2>&1 && echo "tofu:      $$(tofu version | head -1)" || echo "tofu:      missing (make tools-infra)"
	@command -v sops >/dev/null 2>&1 && echo "sops:      $$(sops --version 2>/dev/null | head -1)" || echo "sops:      missing (make tools-infra)"
	@command -v age  >/dev/null 2>&1 && echo "age:       $$(age --version)" || echo "age:       missing (make tools-infra)"

tools:
	$(GO) install honnef.co/go/tools/cmd/staticcheck@latest
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest

tools-infra:
	./scripts/install-infra-tools

## build: build ./bin/forge (static)
build:
	@mkdir -p $(BIN)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/forge ./cmd/forge

## test: unit tests with the race detector
test:
	CGO_ENABLED=1 $(GO) test -race -count=1 $(PKG)

## integration: end-to-end tests (git, ssh, gemini, titan); needs git in PATH
integration: build
	$(GO) test -count=1 -tags integration ./tests/...

fmt:
	gofmt -l -w cmd internal tests

vet:
	$(GO) vet $(PKG)

## lint: format check, vet, staticcheck, govulncheck, infra syntax
lint: vet
	@test -z "$$(gofmt -l cmd internal tests)" || { gofmt -l cmd internal tests; echo 'gofmt: files need formatting'; exit 1; }
	staticcheck $(PKG)
	govulncheck $(PKG)
	$(MAKE) infra-lint

## infra-lint: tofu fmt/validate, bird config check, nftables syntax where tools exist
infra-lint:
	./scripts/infra-lint

## run: run a local forge with a throwaway data directory under ./var
run: build
	./scripts/dev-run

## check: everything CI runs
check: lint test integration

## dist: build release binaries for linux/amd64 and linux/arm64 into ./dist
dist:
	./scripts/release

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN) var coverage.out

help:
	@grep -E '^## ' Makefile | sed 's/^## //'
