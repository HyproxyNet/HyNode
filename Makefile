# Makefile for HyNode

# Build configuration
APP_NAME    := hynode
MODULE      := ./cmd/hynode
BUILD_DIR   := build
BUILD_TAGS  := with_quic with_acme with_utls
GOPROXY     := https://goproxy.cn,direct
GOSUMDB     := off
GOTOOLCHAIN := go1.24.7

# Version info (auto-detected from git)
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME  ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.buildTime=$(BUILD_TIME)

# Common Go environment
GO_ENV := CGO_ENABLED=0 GOTOOLCHAIN=$(GOTOOLCHAIN) GOPROXY=$(GOPROXY) GOSUMDB=$(GOSUMDB)

.PHONY: build build-all test lint clean docker tidy install release help

# Default target
.DEFAULT_GOAL := build

## build: Build linux/amd64 binary
build:
	$(GO_ENV) GOOS=linux GOARCH=amd64 \
		go build -trimpath \
		-tags "$(BUILD_TAGS)" \
		-ldflags '$(LDFLAGS)' \
		-o $(BUILD_DIR)/$(APP_NAME)-linux-amd64 \
		$(MODULE)

## build-all: Build binaries for linux/amd64, linux/arm64, linux/arm
build-all: build
	$(GO_ENV) GOOS=linux GOARCH=arm64 \
		go build -trimpath \
		-tags "$(BUILD_TAGS)" \
		-ldflags '$(LDFLAGS)' \
		-o $(BUILD_DIR)/$(APP_NAME)-linux-arm64 \
		$(MODULE)
	$(GO_ENV) GOOS=linux GOARCH=arm GOARM=7 \
		go build -trimpath \
		-tags "$(BUILD_TAGS)" \
		-ldflags '$(LDFLAGS)' \
		-o $(BUILD_DIR)/$(APP_NAME)-linux-arm \
		$(MODULE)

## test: Run all tests
test:
	$(GO_ENV) go test -tags "$(BUILD_TAGS)" ./...

## lint: Run golangci-lint (if available)
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --build-tags "$(BUILD_TAGS)" ./...; \
	else \
		echo "golangci-lint is not installed, skipping."; \
	fi

## tidy: Run go mod tidy
tidy:
	$(GO_ENV) go mod tidy

## clean: Remove build directory
clean:
	rm -rf $(BUILD_DIR)

## docker: Build Docker image
docker:
	docker build -t $(APP_NAME):latest .

## install: Install binary to /usr/local/bin
install: build
	install -m 755 $(BUILD_DIR)/$(APP_NAME)-linux-amd64 /usr/local/bin/$(APP_NAME)

## release: Build all architectures and create release tarballs
release: build-all
	@for arch in amd64 arm64 arm; do \
		cd $(BUILD_DIR) && tar czf $(APP_NAME)-linux-$$arch.tar.gz $(APP_NAME)-linux-$$arch && cd ..; \
	done
	@echo "Release tarballs created in $(BUILD_DIR)/"

## help: Show this help
help:
	@echo "HyNode Build System"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build       Build linux/amd64 binary (default)"
	@echo "  build-all   Build for linux/amd64, linux/arm64, linux/arm"
	@echo "  test        Run all tests"
	@echo "  lint        Run golangci-lint (if available)"
	@echo "  tidy        Run go mod tidy"
	@echo "  clean       Remove build directory"
	@echo "  docker      Build Docker image"
	@echo "  install     Install binary to /usr/local/bin"
	@echo "  release     Build all architectures and create release tarballs"
	@echo "  help        Show this help"
