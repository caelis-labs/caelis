GIT_TAG ?= $(shell git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null || true)
GIT_DIRTY ?= $(shell test -z "$$(git status --porcelain 2>/dev/null)" || echo dirty)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_VERSION ?= $(if $(and $(strip $(GIT_TAG)),$(filter-out dirty,$(GIT_DIRTY))),$(strip $(GIT_TAG)),dev)
BUILD_KIND ?= $(if $(filter dev,$(BUILD_VERSION)),dev,release)
BUILD_ID ?= $(COMMIT)@$(DATE)
LDFLAGS ?= -X github.com/caelis-labs/caelis/internal/version.Version=$(BUILD_VERSION) -X github.com/caelis-labs/caelis/internal/version.Commit=$(COMMIT) -X github.com/caelis-labs/caelis/internal/version.Date=$(DATE) -X github.com/caelis-labs/caelis/internal/version.BuildID=$(BUILD_ID) -X github.com/caelis-labs/caelis/internal/version.BuildKind=$(BUILD_KIND)
GOFILES_CMD = if command -v rg >/dev/null 2>&1; then rg --files -0 -g '*.go'; else find . -type f -name '*.go' ! -path './.git/*' ! -path './.tmp/*' -print0; fi
GO_TEST_TIMEOUT ?= 5m
ifeq ($(OS),Windows_NT)
BASH ?= $(shell git --exec-path)/../../../bin/bash.exe
else
BASH ?= bash
endif
EVAL_REGRESSION_SELECTOR ?= ^TestRegression
TUI_GOLDEN_SELECTOR ?= ^TestRegressionACPEventstreamToolCallFrame120x32$$
TUI_INTERACTION_SELECTOR ?= ^(TestRegressionACPEventstreamWhitespaceOnlyAssistantChunkDoesNotRenderBeforeTool|TestTypedResumeEnterLoadsEmptyQueryAndSubmitsSelectedSession|TestResumeTabRetriesAfterTransientCompletionFailure|TestHandleACPEventEnvelopeRendersSemanticSpawnEventsOnce|TestHandleACPEventEnvelopeScopedChildTerminalKeepsOneSpawnPanelAndMainTurnAlive)$$
CONTROL_TURN_REGRESSION_SELECTOR ?= ^(TestSessionTurnClientAttachesBeforePromptAndFiltersExactTarget|TestSessionTurnClientRecoversGapFromLastAcceptedCursor|TestSessionTurnClientRoutesApprovalAndCancelWithoutClosingSession)$$
SURFACE_CLIENT_REGRESSION_SELECTOR ?= ^(TestSessionClientAdapterRoutesMainTurnWritesAndObservationThroughTypedClient|TestSessionClientAdapterRoutesReviewThroughTypedParticipantClient|TestSessionClientAdapterRoutesSideAgentStartAndFollowUpThroughTypedClients|TestAppServerAdapterResumeFailurePreservesCurrentSession)$$
COMMAND_REGRESSION_SELECTOR ?= ^TestRegression(Command(Status|Workspace|List|Agent|Parse|Connect|NewDriver)|Slash)
COMMAND_EXECUTION_REGRESSION_SELECTOR ?= ^TestRegressionCommandExec
PRODUCT_TUI_SELECTOR ?= ^TestProductScenarioContextCompactionRuntimeToPhysicalTUI$$
PRODUCT_RUNTIME_SELECTOR ?= ^(TestRuntimeRecoveryConvergesCommandRehydrateFailure|TestRuntimeRecoveryCommandRepairFileStoreRoundTrip|TestSessionWriteQueueCancellationPassesAdmissionToSuccessor|TestSessionWriteQueueCancellationUnlinksBeforePredecessorCompletes|TestTwoRuntimesRejectStaleCompactionAndRebuildWholeModelContext|TestRuntimeAutoCompactFailurePublishesLiveNotice|TestSubagentActivityCursorAdvancesWithStreamEvents|TestTaskControlSnapshotToolResultDescribesSubagentInterruption|TestSubagentCompletionNoticeUsesInterruptionLanguage)$$
PRODUCT_CONTROLLER_SELECTOR ?= ^(TestControllerRunRejectsOverlappingTurns|TestControllerRunConcurrentAdmissionAllowsOneTurn|TestManagerDeactivateCancelsInFlightControllerTurn|TestManagerDetachCancelsInFlightParticipantPrompt)$$
PRODUCT_SUBAGENT_SELECTOR ?= ^(TestRunnerActionSummaryKeepsFinalizingIntentAcrossSparseToolUpdate|TestRunnerActionSummaryConcurrentUpdateAndWait|TestRunnerCompletedResultKeepsActionSummary|TestRunnerTerminalDiagnosticOverridesActionSummary|TestRunnerActionSummaryUsesCanonicalToolContentNotRawOutput|TestSubagentActionSummaryIsBoundedWhitespaceCompactedAndUTF8Safe)$$
PRODUCT_CONTROL_CLIENT_SELECTOR ?= ^(TestStateServiceDoesNotStarveWhileSessionRevisionChanges|TestStateServiceReconnectSucceedsDuringContinuousPublish)$$
PRODUCT_WIRE_SELECTOR ?= ^TestEveryProductionEnvelopeVariantConformsToOpenAPI$$
PRODUCT_DIAGNOSTICS_SELECTOR ?= ^(TestRuntimeDiagnosticsLoggerWritesPrivateJSONLFile|TestBoundedDiagnosticWriterKeepsOneSizeBoundedBackup)$$
# Local and sandboxed runs reuse a stable repository cache. CI keeps its
# standard cache paths so runner-level cache integrations remain effective.
ifeq ($(strip $(CI)),)
CACHE_ROOT ?= $(CURDIR)/.tmp/cache
endif
ifneq ($(strip $(CACHE_ROOT)),)
GOMODCACHE ?= $(CACHE_ROOT)/gomod
GOCACHE ?= $(CACHE_ROOT)/gocache
GOTMPDIR ?= $(CACHE_ROOT)/gotmp
GOLANGCI_LINT_CACHE ?= $(CACHE_ROOT)/golangci-lint
XDG_CACHE_HOME ?= $(CACHE_ROOT)/xdg
export GOMODCACHE GOCACHE GOTMPDIR GOLANGCI_LINT_CACHE XDG_CACHE_HOME
endif
.PHONY: arch-lint build build-cli cache-dirs client-protocol-check client-protocol-generate command-regression command-execution-regression commit-check control-feed-regression docs-links eval-smoke fmt fmt-check guardian-eval install lint product-acceptance quality regression sdk-boundary-check sdk-proxy-smoke sdk-race startup-performance test tui-golden tui-interaction vet release-dry-run

cache-dirs:
ifneq ($(strip $(CACHE_ROOT)),)
	mkdir -p "$(GOMODCACHE)" "$(GOCACHE)" "$(GOTMPDIR)" "$(GOLANGCI_LINT_CACHE)" "$(XDG_CACHE_HOME)"
endif

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
	"$(BASH)" ./scripts/sdk_boundary_check.sh

sdk-proxy-smoke: cache-dirs
	"$(BASH)" ./scripts/sdk_proxy_smoke.sh

sdk-race: cache-dirs
	go test -race -timeout $(GO_TEST_TIMEOUT) ./agent-sdk/policy/... ./agent-sdk/session/... ./agent-sdk/runtime/...

docs-links: cache-dirs
	go run ./scripts/markdown_links

client-protocol-generate: cache-dirs
	go run ./scripts/client_protocol_generate

client-protocol-check: cache-dirs
	go run ./scripts/client_protocol_generate -check

quality: lint test build

commit-check: quality

startup-performance: cache-dirs
	go test -run '^$$' -bench '^BenchmarkNewLocalStackFirstFrameBoundary$$' -benchtime=2x -count=1 ./app/gatewayapp

regression: product-acceptance eval-smoke tui-golden tui-interaction control-feed-regression command-regression command-execution-regression

product-acceptance: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./agent-sdk/runtime '$(PRODUCT_RUNTIME_SELECTOR)' product-runtime
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./internal/acpagentbridge/controller '$(PRODUCT_CONTROLLER_SELECTOR)' product-controller
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./internal/acpagentbridge/subagent '$(PRODUCT_SUBAGENT_SELECTOR)' product-subagent
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./control/appserver '$(PRODUCT_CONTROL_CLIENT_SELECTOR)' product-control-client
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./control/appserver/wirev1 '$(PRODUCT_WIRE_SELECTOR)' product-wire
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./app/gatewayapp '$(PRODUCT_DIAGNOSTICS_SELECTOR)' product-diagnostics
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./surfaces/tui/app '$(PRODUCT_TUI_SELECTOR)' product-tui

eval-smoke: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./eval '$(EVAL_REGRESSION_SELECTOR)' eval-smoke

guardian-eval: cache-dirs
	CAELIS_GUARDIAN_E2E=1 go test -tags=e2e -timeout 30m -run '^TestGuardianLiveE2E$$' -v ./eval

tui-golden: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./surfaces/tui/app '$(TUI_GOLDEN_SELECTOR)' tui-golden

tui-interaction: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./surfaces/tui/app '$(TUI_INTERACTION_SELECTOR)' tui-interaction

control-feed-regression: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./control/appserver '$(CONTROL_TURN_REGRESSION_SELECTOR)' control-turn-regression
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./internal/controlprompt/appserveradapter '$(SURFACE_CLIENT_REGRESSION_SELECTOR)' surface-client-regression

command-regression: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./app/gatewayapp/controladapter '$(COMMAND_REGRESSION_SELECTOR)' command-regression

command-execution-regression: cache-dirs
	GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) "$(BASH)" ./scripts/go_test_nonempty.sh ./app/gatewayapp/controladapter '$(COMMAND_EXECUTION_REGRESSION_SELECTOR)' command-execution-regression

# golangci-lint already runs govet; do not repeat the same analyzers here.
test: cache-dirs
	go test -vet=off -timeout $(GO_TEST_TIMEOUT) ./...

release-dry-run: cache-dirs
	goreleaser release --clean --snapshot
