#!/usr/bin/env bash
set -euo pipefail

if [[ "$(go env GOOS)" != windows || "$(go env GOHOSTOS)" != windows ]]; then
  echo "windows-check requires native Windows execution" >&2
  exit 1
fi

# Keep small platform owners complete; Linux CI covers the general suite.
# Leave implicit vet enabled for Windows-only source files.
go test -count=1 -p=2 -timeout "${GO_TEST_TIMEOUT:-5m}" \
  ./platform/winproc \
  ./internal/filelock ./internal/productpaths ./internal/servicelifecycle \
  ./agent-sdk/atomicfile ./agent-sdk/policy/presets \
  ./agent-sdk/sandbox/consoleoutput ./agent-sdk/sandbox/internal/conpty \
  ./agent-sdk/sandbox/host ./agent-sdk/sandbox/backend/cmdsession \
  ./agent-sdk/sandbox/windows/... \
  ./control/workspacetrust ./control/modelconfig/credentialstore \
  ./app/gatewayapp/internal/configstore ./app/gatewayapp/internal/adapterhost

# Large packages run only native process, storage, path, and clipboard contracts.
# Selectors must match tests, so renames cannot silently remove this coverage.
bash ./scripts/go_test_nonempty.sh ./agent-sdk/session/file \
  '^TestWindows' windows-session-storage -count=1
bash ./scripts/go_test_nonempty.sh ./agent-sdk/runtime \
  '^TestRuntime(CommandTTYDefaultTaskWriteSubmitsWindowsLine|SpawnToolIsParallelSafeAndConcurrentAttachmentsConverge)$' windows-runtime -count=1
bash ./scripts/go_test_nonempty.sh ./control/appserver \
  '^TestSyncOperationStoreDirectoryDoesNotFlushReadOnlyWindowsDirectory$' windows-operation-storage -count=1
bash ./scripts/go_test_nonempty.sh ./app/gatewayapp \
  '^Test(WindowsOpenRouterReconnectPreservesCustomReasoningLevels|HostModelConnectUsesCanonicalDocumentAndDoesNotPersistSecretInLedger|ACPPrepareCommandRecoversIntentOnlyReceiptWithoutRepeatingProcess|NewLocalStackProductionBootstrapDoesNotPersistSandboxNetworkDefault)$' windows-host-persistence -count=1
bash ./scripts/go_test_nonempty.sh ./internal/cli \
  '^TestRunDoctorStartupRepairsWorkspaceIdentityConflict$' windows-workspace-paths -count=1
bash ./scripts/go_test_nonempty.sh ./surfaces/tui/app \
  '^Test.*(Clipboard|NativeWrite|OSC52)' windows-clipboard -count=1

# Match the release configuration for all production packages and Memory Open.
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 GO_TEST_TIMEOUT=10m bash ./scripts/go_test_nonempty.sh \
  ./app/gatewayapp/internal/memoryhost '^TestEmbeddedHostBindsSDKClient$' windows-memory-open -count=1
