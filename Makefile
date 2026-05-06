GIT_TAG ?= $(shell git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null || true)
VERSION ?= $(if $(strip $(GIT_TAG)),$(strip $(GIT_TAG)),$(shell cat VERSION 2>/dev/null || echo dev))
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_VERSION := $(if $(filter command line,$(origin VERSION)),$(VERSION),$(if $(strip $(GIT_TAG)),$(strip $(GIT_TAG)),$(shell cat VERSION 2>/dev/null || echo dev)))
LDFLAGS ?= -X github.com/OnslaughtSnail/caelis/internal/version.Version=$(BUILD_VERSION) -X github.com/OnslaughtSnail/caelis/internal/version.Commit=$(COMMIT) -X github.com/OnslaughtSnail/caelis/internal/version.Date=$(DATE)
GOFILES := $(shell if command -v rg >/dev/null 2>&1; then rg --files -g '*.go'; else find . -type f -name '*.go' | sed 's|^\./||' | LC_ALL=C sort; fi)
.PHONY: build build-cli finish fmt fmt-check install lint quality test test-e2e tidy vet eval-light eval-nightly eval-real-matrix release-dry-run

tidy:
	go mod tidy

fmt:
	gofmt -w $(GOFILES)

fmt-check:
	@test -z "$$(gofmt -l $(GOFILES))"

build:
	go build ./...

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/caelis

build-cli:
	mkdir -p ./.tmp/bin
	go build -ldflags "$(LDFLAGS)" -o ./.tmp/bin/caelis ./cmd/caelis

vet:
	go vet ./...

lint:
	golangci-lint run ./...

quality: fmt-check lint vet test build

finish: tidy fmt quality

test:
	go test ./...

test-e2e:
	go test -tags=e2e ./...

eval-light:
	go run ./eval/cmd -suite light

eval-nightly:
	go run ./eval/cmd -suite nightly

eval-real-matrix:
	go run ./eval/cmd -suite light -models "deepseek-v4-flash,gemini-3.1-flash-lite-preview" -stream-modes both -thinking-modes both -thinking-budget 1024

release-dry-run:
	goreleaser release --clean --snapshot
