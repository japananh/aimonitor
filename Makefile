SHELL := /bin/bash

GO            ?= go
BIN_DIR       := bin
BINARY        := $(BIN_DIR)/aimonitor
PKG           := ./...
VERSION       := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS       := -s -w -X github.com/japananh/aimonitor/internal/version.Version=$(VERSION)

.PHONY: build test lint tidy fmt run clean widget all

all: build

build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/aimonitor

test:
	$(GO) test -race -count=1 $(PKG)

lint:
	@which golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed; see https://golangci-lint.run"; exit 1; }
	golangci-lint run $(PKG)

tidy:
	$(GO) mod tidy

fmt:
	$(GO) fmt $(PKG)

run: build
	$(BINARY)

clean:
	rm -rf $(BIN_DIR) dist

widget:
	@if [ "$$(uname)" != "Darwin" ]; then echo "widget target is macOS-only"; exit 1; fi
	@cd ui/macos && xcodebuild -project AIMonitor.xcodeproj -scheme AIMonitor -configuration Release
