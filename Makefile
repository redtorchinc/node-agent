BINARY      := rt-node-agent
PKG         := github.com/redtorchinc/node-agent
CMD         := ./cmd/rt-node-agent
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)
GIT_SHA     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
               -X $(PKG)/internal/buildinfo.Version=$(VERSION) \
               -X $(PKG)/internal/buildinfo.GitSHA=$(GIT_SHA) \
               -X $(PKG)/internal/buildinfo.BuildTime=$(BUILD_TIME)

DIST        := dist
TARGETS     := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build test vet tidy cross clean run

all: build

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

run: build
	./$(BINARY) run

cross: clean
	@mkdir -p $(DIST)
	@for target in $(TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		out="$(DIST)/$(BINARY)_$${os}_$${arch}$${ext}"; \
		echo "build $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" -o $$out $(CMD) || exit 1; \
	done
	@cd $(DIST) && shasum -a 256 * > SHA256SUMS

clean:
	rm -rf $(BINARY) $(BINARY).exe $(DIST)
