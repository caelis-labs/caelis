GIT_TAG ?= $(shell git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null || true)
GIT_DIRTY ?= $(shell test -z "$$(git status --porcelain 2>/dev/null)" || echo dirty)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_VERSION ?= $(if $(and $(strip $(GIT_TAG)),$(filter-out dirty,$(GIT_DIRTY))),$(strip $(GIT_TAG)),dev)
LDFLAGS ?= -X github.com/caelis-labs/caelis/internal/version.Version=$(BUILD_VERSION) -X github.com/caelis-labs/caelis/internal/version.Commit=$(COMMIT) -X github.com/caelis-labs/caelis/internal/version.Date=$(DATE)
GOFILES_CMD = if command -v rg >/dev/null 2>&1; then rg --files -0 -g '*.go'; else find . -type f -name '*.go' -print0; fi
GO_TEST_TIMEOUT ?= 5m
AGENT_SDK_DIR ?= agent-sdk
RUN_AGENT_SDK = cd $(AGENT_SDK_DIR) && GOWORK=off
CACHE_ROOT ?= $(CURDIR)/.tmp/cache
GOMODCACHE ?= $(CACHE_ROOT)/gomod
GOCACHE ?= $(CACHE_ROOT)/gocache
GOTMPDIR ?= $(CACHE_ROOT)/gotmp
GOLANGCI_LINT_CACHE ?= $(CACHE_ROOT)/golangci-lint
XDG_CACHE_HOME ?= $(CACHE_ROOT)/xdg
export GOMODCACHE GOCACHE GOTMPDIR GOLANGCI_LINT_CACHE XDG_CACHE_HOME
.PHONY: arch-lint build build-cli cache-dirs command-regression command-execution-regression commit-check eval-smoke fmt fmt-check install lint quality regression sdk-external-consumer-check sdk-external-replace-check sdk-standalone-check test tui-golden tui-interaction vet release-dry-run

cache-dirs:
	mkdir -p "$(GOMODCACHE)" "$(GOCACHE)" "$(GOTMPDIR)" "$(GOLANGCI_LINT_CACHE)" "$(XDG_CACHE_HOME)"

fmt:
	$(GOFILES_CMD) | xargs -0 gofmt -w

fmt-check:
	@out="$$($(GOFILES_CMD) | xargs -0 gofmt -l)"; test -z "$$out" || { printf '%s\n' "$$out"; exit 1; }

build: cache-dirs
	@root_status=0; sdk_status=0; \
	go build ./... & root_pid=$$!; \
	( $(RUN_AGENT_SDK) go build ./... ) & sdk_pid=$$!; \
	wait $$root_pid || root_status=$$?; \
	wait $$sdk_pid || sdk_status=$$?; \
	exit $$((root_status || sdk_status))

install: cache-dirs
	go install -ldflags "$(LDFLAGS)" ./cmd/caelis

build-cli: cache-dirs
	mkdir -p ./.tmp/bin
	go build -ldflags "$(LDFLAGS)" -o ./.tmp/bin/caelis ./cmd/caelis

vet: cache-dirs
	go vet ./...
	$(RUN_AGENT_SDK) go vet ./...

lint: cache-dirs
	golangci-lint run ./...
	$(RUN_AGENT_SDK) golangci-lint run ./...

arch-lint: cache-dirs
	go run ./scripts/arch_lint.go --include-tests

sdk-standalone-check: cache-dirs
	./scripts/sdk_standalone_check.sh

sdk-external-replace-check: cache-dirs
	./scripts/sdk_external_replace_check.sh

sdk-external-consumer-check: cache-dirs
	./scripts/sdk_external_consumer_check.sh

quality: fmt-check lint arch-lint vet test sdk-standalone-check sdk-external-replace-check sdk-external-consumer-check

commit-check: quality build

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

test: cache-dirs
	@root_status=0; sdk_status=0; \
	go test -timeout $(GO_TEST_TIMEOUT) ./... & root_pid=$$!; \
	( $(RUN_AGENT_SDK) go test -timeout $(GO_TEST_TIMEOUT) -count=1 ./... ) & sdk_pid=$$!; \
	wait $$root_pid || root_status=$$?; \
	wait $$sdk_pid || sdk_status=$$?; \
	exit $$((root_status || sdk_status))

release-dry-run: quality
	goreleaser release --clean --snapshot
