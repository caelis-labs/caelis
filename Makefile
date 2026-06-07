GOFILES_CMD = if command -v rg >/dev/null 2>&1; then rg --files -0 -g '*.go'; else find . -type f -name '*.go' -print0; fi
CACHE_ROOT ?= $(CURDIR)/.tmp/cache
GOMODCACHE ?= $(CACHE_ROOT)/gomod
GOCACHE ?= $(CACHE_ROOT)/gocache
GOTMPDIR ?= $(CACHE_ROOT)/gotmp
GOLANGCI_LINT_CACHE ?= $(CACHE_ROOT)/golangci-lint
XDG_CACHE_HOME ?= $(CACHE_ROOT)/xdg
export GOMODCACHE GOCACHE GOTMPDIR GOLANGCI_LINT_CACHE XDG_CACHE_HOME

ACTIVE_PKGS = ./...

.PHONY: arch-lint build cache-dirs fmt fmt-check layer4-regression lint quality regression size-report test vet

cache-dirs:
	mkdir -p "$(GOMODCACHE)" "$(GOCACHE)" "$(GOTMPDIR)" "$(GOLANGCI_LINT_CACHE)" "$(XDG_CACHE_HOME)"

fmt:
	$(GOFILES_CMD) | xargs -0 gofmt -w

fmt-check:
	@out="$$($(GOFILES_CMD) | xargs -0 gofmt -l)"; test -z "$$out" || { printf '%s\n' "$$out"; exit 1; }

build: cache-dirs
	go build $(ACTIVE_PKGS)

vet: cache-dirs
	go vet $(ACTIVE_PKGS)

lint: cache-dirs
	golangci-lint run ./...

arch-lint:
	go run ./scripts/arch_lint.go

size-report:
	bash scripts/size_report.sh

quality: fmt-check lint arch-lint vet test build

regression: layer4-regression

layer4-regression: cache-dirs
	go test ./test/e2e/layer4

test: cache-dirs
	go test $(ACTIVE_PKGS)
