BINARY  := perch
PKG     := ./cmd/perch
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Fully static: no cgo, stripped symbol table + DWARF, reproducible paths.
CGO_ENABLED ?= 0
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath -ldflags '$(LDFLAGS)'

# Host platform (override GOOS/GOARCH for cross-compiles).
GOOS   ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

.PHONY: all build build-all test vet fmt tidy run clean

all: build

## build: static binary for the host platform -> ./bin/$(BINARY)
build:
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build $(GOFLAGS) -o bin/$(BINARY) $(PKG)

## build-all: static binaries for common deploy targets
build-all:
	$(MAKE) build GOOS=linux  GOARCH=amd64 && mv bin/$(BINARY) bin/$(BINARY)-linux-amd64
	$(MAKE) build GOOS=linux  GOARCH=arm64 && mv bin/$(BINARY) bin/$(BINARY)-linux-arm64
	$(MAKE) build GOOS=darwin GOARCH=arm64 && mv bin/$(BINARY) bin/$(BINARY)-darwin-arm64

## test: run all tests
test:
	go test ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format sources
fmt:
	gofmt -w .

## tidy: sync go.mod/go.sum
tidy:
	go mod tidy

## run: build then run (pass config via env)
run: build
	./bin/$(BINARY)

## clean: remove build artifacts
clean:
	rm -rf bin
