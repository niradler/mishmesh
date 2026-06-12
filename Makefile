# mishmesh — Makefile
# Usage: make <target>. Run `make help` for the list.

SHELL := /usr/bin/env bash
GO    ?= go
BIN   := bin
PKG   := ./...
GOEXE := $(shell $(GO) env GOEXE)

SERVER_BIN := $(BIN)/mishmesh-server$(GOEXE)
AGENT_BIN  := $(BIN)/mishmesh-agent$(GOEXE)

# Inject version metadata at build time.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := help

## help: show available targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed -e 's/## //' | awk -F': ' '{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## tidy: sync go.mod/go.sum
.PHONY: tidy
tidy:
	$(GO) mod tidy

## fmt: format all Go code
.PHONY: fmt
fmt:
	$(GO) fmt $(PKG)

## vet: run go vet
.PHONY: vet
vet:
	$(GO) vet $(PKG)

## lint: run golangci-lint (install: https://golangci-lint.run)
.PHONY: lint
lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed; skipping"

## test: run all tests with race detector
.PHONY: test
test:
	$(GO) test -race -count=1 $(PKG)

## test-short: run fast unit tests only
.PHONY: test-short
test-short:
	$(GO) test -short -count=1 $(PKG)

## build: build server and agent binaries
.PHONY: build
build: build-server build-agent

.PHONY: build-server
build-server:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(SERVER_BIN) ./cmd/mishmesh-server

.PHONY: build-agent
build-agent:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(AGENT_BIN) ./cmd/mishmesh-agent

## run-server: build and run the server (app mode defaults)
.PHONY: run-server
run-server: build-server
	$(SERVER_BIN)

## check: fmt + vet + test (pre-commit gate)
.PHONY: check
check: fmt vet test

## docker: build the server image
.PHONY: docker
docker:
	docker build -t mishmesh-server:$(VERSION) -f Dockerfile .

## docker-agent: build the agent image
.PHONY: docker-agent
docker-agent:
	docker build -t mishmesh-agent:$(VERSION) -f Dockerfile.agent .

## compose-up: run the local demo stack (server + whoami + agent)
.PHONY: compose-up
compose-up:
	docker compose up --build

## compose-down: stop the demo stack
.PHONY: compose-down
compose-down:
	docker compose down -v

## clean: remove build artifacts
.PHONY: clean
clean:
	rm -rf $(BIN)
