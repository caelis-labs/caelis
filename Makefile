GIT_TAG ?= $(shell git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null || true)
GIT_DIRTY ?= $(shell test -z "$$(git status --porcelain 2>/dev/null)" || echo dirty)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_VERSION ?= $(if $(and $(strip $(GIT_TAG)),$(filter-out dirty,$(GIT_DIRTY))),$(strip $(GIT_TAG)),dev)
LDFLAGS ?= -X github.com/OnslaughtSnail/caelis/internal/version.Version=$(BUILD_VERSION) -X github.com/OnslaughtSnail/caelis/internal/version.Commit=$(COMMIT) -X github.com/OnslaughtSnail/caelis/internal/version.Date=$(DATE)
GOFILES_CMD = if command -v rg >/dev/null 2>&1; then rg --files -0 -g '*.go'; else find . -type f -name '*.go' -print0; fi
GO_TEST_TIMEOUT ?= 5m
CACHE_ROOT ?= $(CURDIR)/.tmp/cache
GOMODCACHE ?= $(CACHE_ROOT)/gomod
GOCACHE ?= $(CACHE_ROOT)/gocache
GOTMPDIR ?= $(CACHE_ROOT)/gotmp
GOLANGCI_LINT_CACHE ?= $(CACHE_ROOT)/golangci-lint
XDG_CACHE_HOME ?= $(CACHE_ROOT)/xdg
export GOMODCACHE GOCACHE GOTMPDIR GOLANGCI_LINT_CACHE XDG_CACHE_HOME
.PHONY: arch-lint bench-regression bench-threshold build build-cli cache-dirs command-regression command-execution-regression eval-smoke fmt fmt-check install lint quality regression size-report test tui-golden tui-interaction tui-bench vet release-dry-run

cache-dirs:
	mkdir -p "$(GOMODCACHE)" "$(GOCACHE)" "$(GOTMPDIR)" "$(GOLANGCI_LINT_CACHE)" "$(XDG_CACHE_HOME)"

fmt:
	$(GOFILES_CMD) | xargs -0 gofmt -w

fmt-check:
	@out="$$($(GOFILES_CMD) | xargs -0 gofmt -l)"; test -z "$$out" || { printf '%s\n' "$$out"; exit 1; }

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

arch-lint:
	go run ./scripts/arch_lint.go

size-report:
	bash scripts/size_report.sh

quality: fmt-check lint vet test build

regression: eval-smoke tui-golden tui-interaction command-regression command-execution-regression

eval-smoke: cache-dirs
	go test -timeout $(GO_TEST_TIMEOUT) ./eval -run 'TestRegression'

tui-golden: cache-dirs
	go test -timeout $(GO_TEST_TIMEOUT) ./surfaces/tui/app -run 'TestRegression.*Golden'

tui-interaction: cache-dirs
	go test -timeout $(GO_TEST_TIMEOUT) ./surfaces/tui/app -run 'TestRegression(Resize|NoWelcome|TerminalOutput|FollowTail|Slash|Approval)'

command-regression: cache-dirs
	go test -timeout $(GO_TEST_TIMEOUT) ./app/gatewayapp/controladapter -run 'TestRegression(Command(Status|Workspace|List|Agent|Parse|Connect|NewDriver)|Slash)'

command-execution-regression: cache-dirs
	go test -timeout $(GO_TEST_TIMEOUT) ./app/gatewayapp/controladapter -run 'TestRegressionCommandExec'

tui-bench: cache-dirs
	CAELIS_BENCH_REGRESSION=1 go test ./surfaces/tui/app -run 'TestRegressionBenchThresholds' -v

bench-regression: cache-dirs
	go test -timeout $(GO_TEST_TIMEOUT) ./surfaces/tui/app -run '^$$' -bench 'Benchmark(ViewportSyncLongTranscript|AssistantTailIncrementalSync|AssistantStablePrefixTailMarkdownStream|ToolOutputStream10kChunks|VisibleSelectionRenderLongTranscript|RenderSchedulerMixedStreams)' -benchmem

test: cache-dirs
	go test -timeout $(GO_TEST_TIMEOUT) ./...

release-dry-run: cache-dirs
	goreleaser release --clean --snapshot
