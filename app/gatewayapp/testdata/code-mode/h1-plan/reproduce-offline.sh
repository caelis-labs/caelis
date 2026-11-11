#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/../../../../.."
TASK_GOROOT="${CAELIS_H1_GOROOT:-/Users/xueyongzhi/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.10.darwin-arm64}"
export GOROOT="$TASK_GOROOT" GOTOOLCHAIN=local GOWORK=off GOSUMDB=off GOPROXY=off
export PATH="$GOROOT/bin:$PATH"
export CACHE_ROOT="$PWD/.tmp/cache" GOCACHE="$PWD/.tmp/cache/gocache"
export GOMODCACHE="${CAELIS_H1_GOMODCACHE:-/Users/xueyongzhi/go/pkg/mod}"
TASK_EVIDENCE="${CAELIS_H1_REPRO_DIR:-$PWD/.tmp/code-mode-h1-repro-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$TASK_EVIDENCE"
CAELIS_H1_EVIDENCE="$TASK_EVIDENCE" go test ./agent-sdk/runtime -run '^TestJSExecH1' -v -count=1 -timeout=4m > "$TASK_EVIDENCE/offline.txt" 2>&1
go test -race ./agent-sdk/runtime -run '^TestJSExec(H1SessionReuseAndPermissionRevocation|H1HintWarningOncePreservesCapability|H1SearchShapeError|RealGojaDiscoveryAndStableTools|StaleSchemaRejectsOldArgumentsBeforeEffect|BlocksNativeProcessAliasesAndKeepsErrors)$' -v -count=1 -timeout=5m > "$TASK_EVIDENCE/race.txt" 2>&1
make commit-check docs-links > "$TASK_EVIDENCE/gates.txt" 2>&1
printf '%s\n' "$TASK_EVIDENCE"
