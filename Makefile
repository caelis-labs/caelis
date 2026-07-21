GIT_TAG ?= $(shell git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null || true)
GIT_DIRTY ?= $(shell test -z "$$(git status --porcelain 2>/dev/null)" || echo dirty)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_VERSION ?= $(if $(and $(strip $(GIT_TAG)),$(filter-out dirty,$(GIT_DIRTY))),$(strip $(GIT_TAG)),dev)
LDFLAGS ?= -X github.com/caelis-labs/caelis/internal/version.Version=$(BUILD_VERSION) -X github.com/caelis-labs/caelis/internal/version.Commit=$(COMMIT) -X github.com/caelis-labs/caelis/internal/version.Date=$(DATE)
GOFILES_CMD = if command -v rg >/dev/null 2>&1; then rg --files -0 -g '*.go'; else find . -type f -name '*.go' -print0; fi
GO_TEST_TIMEOUT ?= 5m
EVAL_REGRESSION_SELECTOR ?= ^TestRegression
TUI_GOLDEN_SELECTOR ?= ^TestRegressionACPEventstreamToolCallFrame120x32$$
TUI_INTERACTION_SELECTOR ?= ^(TestRegressionACPEventstreamWhitespaceOnlyAssistantChunkDoesNotRenderBeforeTool|TestTypedResumeEnterLoadsEmptyQueryAndSubmitsSelectedSession|TestResumeTabRetriesAfterTransientCompletionFailure|TestHandleACPEventEnvelopeRendersSemanticSpawnEventsOnce|TestHandleACPEventEnvelopeScopedChildTerminalKeepsOneSpawnPanelAndMainTurnAlive)$$
CONTROL_FEED_REGRESSION_SELECTOR ?= ^(TestLiveFeedBrokerFansInSpawnSemanticsInOrder|TestGatewayTurnDoesNotAttachPreparedFeedBeforeSurfaceClaimsEvents|TestLiveFeedBrokerSharesPhysicalSpawnStreamAcrossTaskWaitObservers|TestControlSessionFeedRecoversDetachedChildGapAcrossTurnBrokers|TestLiveFeedBrokerFailsTurnAfterBoundedPermanentRecorderErrors)$$
COMMAND_REGRESSION_SELECTOR ?= ^TestRegression(Command(Status|Workspace|List|Agent|Parse|Connect|NewDriver)|Slash)
COMMAND_EXECUTION_REGRESSION_SELECTOR ?= ^TestRegressionCommandExec
CACHE_ROOT ?= $(CURDIR)/.tmp/cache
GOMODCACHE ?= $(CACHE_ROOT)/gomod
GOCACHE ?= $(CACHE_ROOT)/gocache
GOTMPDIR ?= $(CACHE_ROOT)/gotmp
GOLANGCI_LINT_CACHE ?= $(CACHE_ROOT)/golangci-lint
XDG_CACHE_HOME ?= $(CACHE_ROOT)/xdg
export GOMODCACHE GOCACHE GOTMPDIR GOLANGCI_LINT_CACHE XDG_CACHE_HOME
.PHONY: arch-lint build build-cli cache-dirs client-protocol-check client-protocol-generate command-regression command-execution-regression commit-check control-feed-regression docs-links eval-smoke fmt fmt-check guardian-eval install lint quality regression sdk-boundary-check sdk-proxy-smoke sdk-race test tui-golden tui-interaction vet release-dry-run

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

arch-lint: cache-dirs
	go run ./scripts/arch_lint.go --include-tests

sdk-boundary-check: cache-dirs
	./scripts/sdk_boundary_check.sh

sdk-proxy-smoke: cache-dirs
	./scripts/sdk_proxy_smoke.sh

sdk-race: cache-dirs
	go test -race -timeout $(GO_TEST_TIMEOUT) ./agent-sdk/policy/... ./agent-sdk/session/... ./agent-sdk/runtime/...

docs-links: cache-dirs
	go run ./scripts/markdown_links

client-protocol-generate: cache-dirs
	go run ./scripts/client_protocol_generate

client-protocol-check: cache-dirs
	go run ./scripts/client_protocol_generate -check

quality: fmt-check lint arch-lint sdk-boundary-check client-protocol-check vet test

commit-check: quality build

regression: eval-smoke tui-golden tui-interaction control-feed-regression command-regression command-execution-regression

eval-smoke: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) ./scripts/go_test_nonempty.sh ./eval '$(EVAL_REGRESSION_SELECTOR)' eval-smoke

guardian-eval: cache-dirs
	CAELIS_GUARDIAN_E2E=1 go test -tags=e2e -timeout 30m -run '^TestGuardianLiveE2E$$' -v ./eval

tui-golden: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) ./scripts/go_test_nonempty.sh ./surfaces/tui/app '$(TUI_GOLDEN_SELECTOR)' tui-golden

tui-interaction: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) ./scripts/go_test_nonempty.sh ./surfaces/tui/app '$(TUI_INTERACTION_SELECTOR)' tui-interaction

control-feed-regression: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) ./scripts/go_test_nonempty.sh ./app/gatewayapp/controladapter '$(CONTROL_FEED_REGRESSION_SELECTOR)' control-feed-regression

command-regression: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) ./scripts/go_test_nonempty.sh ./app/gatewayapp/controladapter '$(COMMAND_REGRESSION_SELECTOR)' command-regression

command-execution-regression: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) ./scripts/go_test_nonempty.sh ./app/gatewayapp/controladapter '$(COMMAND_EXECUTION_REGRESSION_SELECTOR)' command-execution-regression

test: cache-dirs
	go test -timeout $(GO_TEST_TIMEOUT) ./...

release-dry-run: quality
	goreleaser release --clean --snapshot
