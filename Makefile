GIT_TAG ?= $(shell git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null || true)
GIT_DIRTY ?= $(shell test -z "$$(git status --porcelain 2>/dev/null)" || echo dirty)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_VERSION ?= $(if $(and $(strip $(GIT_TAG)),$(filter-out dirty,$(GIT_DIRTY))),$(strip $(GIT_TAG)),dev)
LDFLAGS ?= -X github.com/OnslaughtSnail/caelis/internal/version.Version=$(BUILD_VERSION) -X github.com/OnslaughtSnail/caelis/internal/version.Commit=$(COMMIT) -X github.com/OnslaughtSnail/caelis/internal/version.Date=$(DATE)
GOFILES := $(shell if command -v rg >/dev/null 2>&1; then rg --files -g '*.go'; else find . -type f -name '*.go' | sed 's|^\./||' | LC_ALL=C sort; fi)
CACHE_ROOT ?= $(CURDIR)/.tmp/cache
GOMODCACHE ?= $(CACHE_ROOT)/gomod
GOCACHE ?= $(CACHE_ROOT)/gocache
GOTMPDIR ?= $(CACHE_ROOT)/gotmp
GOLANGCI_LINT_CACHE ?= $(CACHE_ROOT)/golangci-lint
XDG_CACHE_HOME ?= $(CACHE_ROOT)/xdg
export GOMODCACHE GOCACHE GOTMPDIR GOLANGCI_LINT_CACHE XDG_CACHE_HOME
.PHONY: build build-cli cache-dirs fmt fmt-check install lint quality test vet release-dry-run

cache-dirs:
	mkdir -p "$(GOMODCACHE)" "$(GOCACHE)" "$(GOTMPDIR)" "$(GOLANGCI_LINT_CACHE)" "$(XDG_CACHE_HOME)"

fmt:
	gofmt -w $(GOFILES)

fmt-check:
	@test -z "$$(gofmt -l $(GOFILES))"

build: cache-dirs
	go build ./...

install: cache-dirs
	go install -ldflags "$(LDFLAGS)" ./cmd/caelis

build-cli: cache-dirs
	mkdir -p ./.tmp/bin
	go build -ldflags "$(LDFLAGS)" -o ./.tmp/bin/caelis ./cmd/caelis

vet: cache-dirs
	go vet ./...

lint: cache-dirs
	golangci-lint run ./...

quality: fmt-check lint vet test build

test: cache-dirs
	go test ./...

release-dry-run: cache-dirs
	goreleaser release --clean --snapshot
