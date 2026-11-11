#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/../../../../.."
TASK_GOROOT="${CAELIS_H1_GOROOT:-/Users/xueyongzhi/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.10.darwin-arm64}"
export GOROOT="$TASK_GOROOT" GOTOOLCHAIN=local GOWORK=off GOSUMDB=off GOPROXY=off
export PATH="$GOROOT/bin:$PATH" CACHE_ROOT="$PWD/.tmp/cache" GOCACHE="$PWD/.tmp/cache/gocache"
export GOMODCACHE="${CAELIS_H1_GOMODCACHE:-/Users/xueyongzhi/go/pkg/mod}"
TASK_EVIDENCE="${CAELIS_H1_REPRO_DIR:-$PWD/.tmp/code-mode-lookup-repro-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$TASK_EVIDENCE"
CAELIS_H1_REVIEW_LOOKUP=0 CAELIS_H1_REVIEW_EVIDENCE="$TASK_EVIDENCE/fallback" go test ./agent-sdk/runtime -run '^TestH1ReviewNativeJSDefinitionCosts/(native|js)/2$' -v -count=1 -timeout=4m > "$TASK_EVIDENCE/fallback.txt" 2>&1
CAELIS_H1_REVIEW_LOOKUP=1 CAELIS_H1_REVIEW_EVIDENCE="$TASK_EVIDENCE/indexed" go test ./agent-sdk/runtime -run '^TestH1ReviewNativeJSDefinitionCosts/(native|js)/2$' -v -count=1 -timeout=4m > "$TASK_EVIDENCE/indexed.txt" 2>&1
CAELIS_H1_EVIDENCE="$TASK_EVIDENCE/h1" go test ./agent-sdk/runtime -run '^TestJSExecH1(SessionReuseAndPermissionRevocation|HintWarningOncePreservesCapability|SearchShapeError|UnknownSearchEmptyGuard)$' -v -count=1 -timeout=4m > "$TASK_EVIDENCE/h1.txt" 2>&1
CAELIS_LOOKUP_EVIDENCE="$TASK_EVIDENCE/manager" go test -race ./agent-sdk/runtime ./agent-sdk/runtime/chat ./agent-sdk/tool/mcp ./agent-sdk/tool/builtin/toolsearch -run '^Test(JSExecLookupStillFiltersAndWrapsSourceTarget|JSExecManagerLookupFinalAdmissionRejectsSourceChange|JSExecH1SessionReuseAndPermissionRevocation|JSExecStaleSchemaRejectsOldArgumentsBeforeEffect|JSExecBlocksNativeProcessAliasesAndKeepsErrors|JSExecYieldApprovalGuardAndTermination|JSExecFinalBoundaryCancelsBeforeJoinAndPreservesUnknown|MCPFixedUnknownRecoveryKeepsOuterCallWithoutReplay|MCPFixedRoutingRefreshDuringApprovalAndRevocation|BindMCPCallsBuildsCatalogOnlyForResolvedDeclaration|SameBatchSearchCannotAuthorizeUndiscoveredSibling|ExactNamedLookupRevalidatesScopeAndDefinition|CatalogCollisionOwnerDoesNotTransferAfterRemoval|ManagerPublishedDefinitionsRemainImmutableAfterAliasMerge)$' -v -count=1 -timeout=6m > "$TASK_EVIDENCE/race.txt" 2>&1
make commit-check docs-links arch-lint sdk-boundary-check > "$TASK_EVIDENCE/gates.txt" 2>&1
printf '%s\n' "$TASK_EVIDENCE"
