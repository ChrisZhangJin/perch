BINARY  := perch
PKG     := ./cmd/perch
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Fully static: no cgo, stripped symbol table + DWARF, reproducible paths.
CGO_ENABLED ?= 0
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath -ldflags '$(LDFLAGS)'

# Compress the resulting binary with UPX (https://upx.github.io/). UPX
# shrinks our static, stripped Go binary by ~60% (typical 7 MB -> ~2.8 MB on
# linux/amd64) at the cost of a tiny startup decompression pass. Set
# PERCH_NO_UPX=1 to skip compression (CI / cases where UPX is unavailable).
ifeq ($(PERCH_NO_UPX),1)
UPX :=
else
UPX ?= $(shell command -v upx 2>/dev/null)
endif
UPX_FLAGS ?= --best --no-color

# Host platform (override GOOS/GOARCH for cross-compiles).
GOOS   ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

.PHONY: all build build-all build-mailtest build-testmode compress test vet fmt tidy run clean

all: build

## build: static binary for the host platform -> ./bin/$(BINARY)
## If UPX is installed (and PERCH_NO_UPX!=1) the binary is compressed in place.
build:
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build $(GOFLAGS) -o bin/$(BINARY) $(PKG)
	@if [ -n "$(UPX)" ]; then \
		echo "upx $(UPX_FLAGS) bin/$(BINARY)"; \
		$(UPX) $(UPX_FLAGS) bin/$(BINARY); \
	else \
		echo "upx not found on PATH; skipping compression. Install upx-ucl or set PERCH_NO_UPX=1 to silence this."; \
	fi

## build-all: static + compressed binaries for common deploy targets
build-all:
	$(MAKE) build GOOS=linux  GOARCH=amd64 && mv bin/$(BINARY) bin/$(BINARY)-linux-amd64
	$(MAKE) build GOOS=linux  GOARCH=arm64 && mv bin/$(BINARY) bin/$(BINARY)-linux-arm64
	$(MAKE) build GOOS=darwin GOARCH=arm64 && mv bin/$(BINARY) bin/$(BINARY)-darwin-arm64

## build-mailtest: static mailtest harness CLI -> ./bin/mailtest
build-mailtest:
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build $(GOFLAGS) -o bin/mailtest ./cmd/mailtest
	@if [ -n "$(UPX)" ]; then \
		echo "upx $(UPX_FLAGS) bin/mailtest"; \
		$(UPX) $(UPX_FLAGS) bin/mailtest; \
	else \
		echo "upx not found on PATH; skipping compression."; \
	fi

## build-testmode: static perch binary with --testmode HTTP injection endpoint -> ./bin/perch-testmode
build-testmode:
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -tags testmode $(GOFLAGS) -o bin/perch-testmode $(PKG)
	@if [ -n "$(UPX)" ]; then \
		echo "upx $(UPX_FLAGS) bin/perch-testmode"; \
		$(UPX) $(UPX_FLAGS) bin/perch-testmode; \
	else \
		echo "upx not found on PATH; skipping compression."; \
	fi

## compress: run UPX --best on an already-built ./bin/perch in place
compress:
	@if [ -z "$(UPX)" ]; then \
		echo "upx not found on PATH. Install upx-ucl (apt-get install upx-ucl) and retry." >&2; \
		exit 1; \
	fi
	@if [ ! -f bin/$(BINARY) ]; then \
		echo "no binary at bin/$(BINARY); run 'make build' first" >&2; \
		exit 1; \
	fi
	$(UPX) $(UPX_FLAGS) bin/$(BINARY)

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
