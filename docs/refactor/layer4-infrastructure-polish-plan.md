# Layer 4 Infrastructure Polish Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` (recommended) or
> `superpowers:executing-plans` to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring Layer 4 from a tested runtime foundation to production-parity
infrastructure for Gateway, covering providers, sandbox backends, compaction,
delegation, approvals, and ACP-facing infrastructure.

**Architecture:** Layer 4 remains the runtime infrastructure boundary:
`session`, `model`, `tool`, `sandbox`, `policy`, `agent`, and `runner` own
model-visible state, execution semantics, and durable replay. Layer 3 Gateway
may orchestrate turns, approvals, cancellation, and projection, but must not
recreate provider, sandbox, compaction, task, or subagent infrastructure.

**Tech Stack:** Go, `iter.Seq2`, Caelis domain packages, old production
implementations from `main` as behavioral references, `go test`,
`git diff --check`, and architecture-lint checks added in this plan.

---

## Current Findings

At plan start, the post-`7954a09e` review and current code agreed on the main
state:

- Layer 4 has a unified `tool.Executor` path, runner-owned wrappers, spawn/task
  basics, hooks, tracer injection, skills/plugins/MCP wiring, and passing
  Layer 4 unit/E2E coverage.
- `runner` imported `tool/builtin/spawn`, violating the built-in layering rule
  documented in `docs/refactor/builtin-layering.md`.
- `runner` still selects `backends[0]` from `sandbox.Factory.Available`.
- `runner.LLMCompactor` does not call its configured LLM and is currently a
  heuristic truncating compactor with a misleading name.
- At plan start, `impl/model/providers`, `impl/sandbox`,
  `impl/agent/acp/subagent`, and `impl/approval/agentreview` still contained
  production behavior that Layer 4 or Layer 3 composition needed before the old
  runtime could be retired.
- After Layer 4 closure, the rewrite branch removed legacy production roots
  from the active module. Use `../caelis-main-reference` or `main` for
  behavioral comparison instead of importing old code back into this branch.
- `session/file` exists in the new layer. The old `impl/session/file`
  durability behavior was used as a reference source during migration.

## Target Boundary

Layer 4 should own these production-capable infrastructure packages:

- `model/providers/`: OpenAI, Anthropic, Gemini, OpenRouter, OpenAI-compatible,
  Ollama, DeepSeek, MiniMax, Volcengine, CodeFree, shared SSE/HTTP/auth helpers,
  and replay metadata mapping.
- `sandbox/{host,seatbelt,bwrap,landlock,windows}/`: platform sandboxes,
  backend routing, diagnostics, and setup/fix support exposed through
  `sandbox.Factory`.
- `runner/`: invocation orchestration, cancellation-aware task/spawn lifecycle,
  compaction, tool wrapper chain, transient streaming events, and replay-safe
  persistence.
- `agent/llmagent/`: model loop only, including stream delta emission, invalid
  tool-call repair, concurrent tool execution, and provider replay metadata.
- `tool/builtin/*`: model-visible declarations and narrow execution adapters,
  without importing `runner`, ACP clients, Gateway, or UI code.
- `protocol/acp/*`: protocol schema, JSON-RPC transport, terminal helpers,
  client transport, and projector semantics. External ACP input must normalize
  to canonical `session.Event` before durable storage.
- `policy/` plus `approval` adapters: policy decisions and approval request
  contracts; auto-review can live in Layer 3 composition only if it depends on
  Gateway state, but the reviewer implementation should not depend on UI.

## Delivery Order

### Slice 1: Remove Layer 4 Boundary Violations

**Purpose:** Make the current Layer 4 DAG enforceable before more migration
work increases coupling.

**Files:**

- Modify: `agent/delegation.go` or `tool/delegation.go`
- Modify: `runner/runner.go`
- Modify: `runner/spawn.go`
- Modify: `runner/toolwrap.go`
- Modify: `tool/builtin/spawn/tools.go`
- Modify: `tool/builtin/spawn/tools_test.go`
- Modify: `runner/runner_test.go`
- Modify: `docs/refactor/architecture-boundaries.md`
- Create or modify: architecture-lint test/script used by `make quality`

**Steps:**

- [x] Add a narrow delegation contract outside `tool/builtin/spawn`, for
  example `agent.Delegator`, `agent.SpawnRequest`, and `agent.SpawnResult`.
- [x] Update `tool/builtin/spawn` to depend on that contract only for invoking
  delegation while keeping tool declaration and argument validation local.
- [x] Update `runner.Config.SpawnDelegator`,
  `newRunnerSpawnDelegator`, and `AugmentTools` to use the new contract.
- [x] Remove all non-test `runner` imports of `tool/builtin/spawn`.
- [x] Add an architecture test that fails if `runner` imports
  `tool/builtin/*`, `protocol/acp`, `app`, `gateway`, `tui`, or `impl`.
- [x] Run `go test ./runner/... ./tool/builtin/spawn/... ./agent/...`.
- [x] Run `git diff --check`.

**Acceptance:** `runner` no longer imports built-in tool subpackages directly,
and the new lint catches regressions.

### Slice 2: Make Runner Cancellation And Spawn Lifecycle Real

**Purpose:** Ensure Layer 3 `Cancel` can interrupt active model calls, tool
calls, child runs, and task-backed spawn flows.

**Files:**

- Modify: `runner/runner.go`
- Modify: `runner/spawn.go`
- Modify: `runner/task.go`
- Modify: `runner/task_store.go`
- Modify: `runner/runner_test.go`
- Modify: `runner/task_test.go`
- Modify: `test/e2e/layer4/e2e_test.go`

**Steps:**

- [x] Thread the parent invocation context through `newRunnerSpawnDelegator`
  and child task execution.
- [x] Store child cancel functions in task state without making task snapshots
  non-serializable.
- [x] Make `TASK cancel` cancel the child run context before marking task
  state canceled.
- [x] Ensure sandbox command execution receives the same cancellable context.
- [x] Add a unit test where parent cancellation stops a child run before its
  model emits a final assistant event.
- [x] Add a Layer 4 E2E for spawn cancel semantics.
- [x] Add a Layer 4 E2E for spawn restart/continue semantics.
- [x] Run `go test ./runner/... ./test/e2e/layer4`.
- [x] Run `git diff --check`.

**Acceptance:** Canceling an invocation or task interrupts active child work
and does not persist a misleading successful assistant result.

### Slice 3: Sandbox Backend Routing And Platform Migration

**Purpose:** Replace `backends[0]` with explicit routing and migrate old
production sandbox implementations into the new `sandbox` domain.

**Files:**

- Modify: `sandbox/types.go`
- Modify: `sandbox/backend.go`
- Modify: `sandbox/session.go`
- Modify: `runner/runner.go`
- Modify: `app/runtime.go`
- Migrate from: `impl/sandbox/host/*`
- Migrate from: `impl/sandbox/seatbelt/*`
- Migrate from: `impl/sandbox/bwrap/*`
- Migrate from: `impl/sandbox/landlock/*`
- Migrate from: `impl/sandbox/windows/*`
- Create: `sandbox/seatbelt/`
- Create: `sandbox/bwrap/`
- Create: `sandbox/landlock/`
- Create: `sandbox/windows/`
- Test: `sandbox/...`
- Test: `runner/runner_test.go`

**Steps:**

- [x] Add `sandbox.RouteRequest` with workspace root, requested backend name,
  platform, policy constraints, and metadata-derived preference.
- [x] Add `sandbox.Factory.Route(ctx, RouteRequest)` or a small
  `sandbox.Router` interface used by runner.
- [x] Make runner request a backend by policy/metadata instead of indexing the
  first available backend.
- [x] Preserve fail-closed behavior when no backend satisfies constraints.
- [x] Migrate host process behavior from `impl/sandbox/host` to `sandbox/host`
  and keep existing tests green.
- [x] Migrate macOS seatbelt implementation to `sandbox/seatbelt`.
  Current migration covers the Layer 4 `sandbox.Backend` sync command,
  filesystem, status, and unsupported-platform contract; async seatbelt parity
  remains part of later platform hardening.
- [x] Migrate Linux bubblewrap and Landlock implementations to `sandbox/bwrap`
  and `sandbox/landlock`.
  Current migration covers package boundaries, `sandbox.Backend` sync command
  contracts, filesystem, status, and unsupported-platform behavior; full Linux
  bwrap mount-policy and Landlock syscall enforcement parity remains later
  platform hardening.
- [x] Migrate Windows workspace-write implementation to `sandbox/windows`.
  Current migration covers package boundaries, `sandbox.Backend` sync command
  contract, filesystem, status, and unsupported-platform behavior; restricted
  token ACL and Windows e2e parity remains later platform hardening.
- [x] Wire `app.NewRuntime` to expose multiple Layer 4 sandbox backends through
  an ordered `sandbox.Factory`, while retaining the single-backend shorthand.
- [x] Keep CLI setup/fix/reset/clean composition in `app/` or command surfaces;
  do not move CLI behavior into sandbox contracts.
- [x] Run `go test ./sandbox/... ./runner/... ./test/e2e/layer4`.
- [x] Run platform-specific tests on available OS targets.
- [x] Run `git diff --check`.

**Acceptance:** Runner creates the intended sandbox backend deterministically,
platform backends live under `sandbox/`, and the Layer 4 runtime entry can be
composed without `impl/sandbox`. Legacy `gatewayapp` setup/fix/reset behavior
is reference material from `main`; upper-layer rewrites should port only the
needed semantics into new Layer 1-3 packages.

**Slice 3 Evidence:** `sandbox` package contracts remain runtime-focused:
backend creation/routing, command execution, filesystem, status, and
constraints. CLI setup/fix/reset/clean composition remains in app/control
surfaces. Architecture lint now rejects `sandbox/*` imports of `app`, `cmd`,
`protocol`, `impl`, or `surfaces`, preventing CLI composition from moving into
Layer 4 sandbox contracts. On the available darwin/arm64 target,
`go test ./sandbox/...` passes across host, seatbelt, unsupported Linux/Windows
backend contracts, and shared sandbox helpers.

### Slice 4: Provider Migration And Replay Metadata Parity

**Purpose:** Move production model providers into Layer 4 and preserve
provider-specific replay semantics through durable session reconstruction.

**Files:**

- Migrate from: `impl/model/providers/*`
- Modify: `model/providers/*`
- Modify: `model/request.go`
- Modify: `model/message.go`
- Modify: `session/projection.go`
- Modify: `session/semantic.go`
- Modify: `session/session_test.go`
- Modify: `app/runtime.go`
- Modify: Layer 3 composition placeholders once the upper layers are rewritten
- Test: `model/providers/...`
- Test: `session/...`
- Test: `test/e2e/layer4/e2e_test.go`

**Steps:**

- [x] Decide the canonical Layer 4 stream event shape. Avoid supporting both
  old `model.StreamEvent` and new `model.ResponseEvent` in production paths.
- [x] Port shared provider helpers first: HTTP client, SSE parser, HTTP error
  mapping, attribution, tool arg normalization, usage mapping, and discovery.
- [x] Port shared SSE parsing, first-event timeout, retryable timeout error,
  HTTP status error mapping, and context-overflow detection into
  `model/providers`, and route the current OpenAI provider through them.
- [x] Port OpenAI-compatible usage mapping into `model/providers`, including
  cached input tokens, reasoning tokens, provider field variants, and derived
  total tokens; extend Layer 4 `model.Usage` accordingly.
- [x] Port shared tool argument normalization into `model/providers`, including
  raw JSON preservation, empty-object defaults, non-object rejection, and
  OpenAI provider request/stream integration.
- [x] Port shared HTTP client/config defaults into `model/providers`, including
  long-request-friendly transports, custom client injection, base URL
  normalization, and OpenAI default base URL wiring.
- [x] Port OpenAI-compatible model discovery into `model/providers`, including
  `/models` request wiring, auth/configured headers, timeout handling, status
  error reuse, model normalization, capability merging, and catalog mapping.
- [x] Add Layer 4 `model.Request` reasoning controls and wire provider-neutral
  reasoning parts as provider-native `reasoning_content` instead of degrading
  them to ordinary assistant text.
- [x] Add Layer 4 `model.Request` output controls and wire OpenAI-compatible
  structured output profiles, including generic `json_schema`, JSON mode,
  schema max output tokens, and DeepSeek `json_object` fallback strategy.
- [x] Add the Layer 4 OpenAI-compatible provider construction path and the
  first DeepSeek profile with provider defaults, configured headers, shared
  stream/error/usage/tool-args behavior, discovery wrapper, thinking payload,
  reasoning-effort mapping, assistant `reasoning_content` replay, and DeepSeek
  token clamping.
- [x] Add the Layer 4 OpenRouter profile with default base URL, attribution
  headers with caller override, discovery wrapper, OpenRouter-native
  `models`/`route`/`transforms`/`provider`/`plugins` payload support, model ID
  normalization, OpenAI-compatible reasoning payloads, assistant `reasoning`
  replay, and streamed `reasoning` delta handling.
- [x] Add Layer 4 Mimo and Volcengine OpenAI-compatible profiles with default
  endpoints, discovery wrappers, `json_object` structured-output strategy,
  provider-native `thinking` payload mapping, assistant `reasoning_content`
  replay, and empty reasoning replay for assistant tool-call loops.
- [x] Add the Layer 4 MiniMax provider over its Anthropic-compatible Messages
  API, including default endpoint/auth/max-output settings, `/v1/models`
  discovery, Anthropic content block request mapping, thinking budget payload,
  streamed text/thinking/tool-use events, usage mapping, replay-token metadata,
  and `ConfiguredFactory` registration.
- [x] Add the Layer 4 CodeFree chat provider with explicit SDK credentials,
  CodeFree-native headers/session metadata, chat and version discovery
  endpoints, OpenAI-compatible request mapping, JSON and SSE response handling,
  JSON fallback for streaming calls, usage/finish mapping, redacted provider
  error summaries, retCode 51 backpressure classification, and
  `ConfiguredFactory` registration.
- [x] Move CodeFree OAuth/login/refresh credential management out of `impl`
  into a Layer 4 auth helper surface without coupling model providers to local
  CLI credential files.
  Current Layer 4 core exposes injected `CodeFreeCredentialStore` and
  `CodeFreeCredentialRefresher` contracts, decrypts cached API keys, refreshes
  expired records, persists refreshed records through the injected store, and
  returns explicit `CodeFreeCredentials` for provider config. Browser OAuth and
  local CLI credential-file adapters remain app/surface composition work.
- [x] Add the Layer 4 Anthropic provider over the official Messages API,
  including default endpoint/auth/max-output settings, `/v1/models` discovery,
  system/tool/tool-result/thinking request block mapping, thinking-budget
  payloads, non-stream content block handling, streamed text/thinking/tool-use
  events, usage/finish mapping, thinking signature replay metadata, and
  `ConfiguredFactory` registration.
- [x] Port Gemini with native `generateContent`/`streamGenerateContent`
  request mapping, `/models` discovery, pre-3 thinking budget and Gemini 3+
  thinking level mapping, structured-output schema controls, tool
  declarations/results, thought signature replay metadata, and
  `ConfiguredFactory` registration.
- [x] Add the Layer 4 Ollama provider with native `/api/chat` request mapping,
  `/api/tags` discovery, `/v1` base URL normalization, structured-output
  `format`, `think` reasoning control, NDJSON streaming, local usage mapping,
  tool-call conversion, image payload forwarding, and `ConfiguredFactory`
  registration.
- [x] Add Layer 4 canonical replay metadata fields to `model.Part`,
  `model.Reasoning`, `model.ToolUse`, and durable `session.EventPart`.
- [x] Update `session.ModelContextFromEvents` so provider replay metadata is
  consumed when rebuilding model requests, not only stored or projected.
- [x] Keep `agent/llmagent` same-invocation history and the current OpenAI
  provider mapper compatible with reasoning parts so runtime and reload
  contexts do not diverge.
- [x] Add a Layer 4 `model/providers.ConfiguredFactory` registration surface
  that exposes migrated providers to `model/catalog`, resolves aliases, returns
  model metadata, normalizes provider/model IDs, and fails closed for providers
  not implemented in the active Layer 4 package.
- [x] Add mocked HTTP/SSE provider tests equivalent to the old
  `impl/model/providers` coverage.
- [x] Add a Layer 4 replay test that round-trips Anthropic-style thought
  signature metadata through `session.Event` back into `model.Message`.
- [x] Run `go test ./model/... ./session/... ./app/...`.
- [x] Run `git diff --check`.

**Acceptance:** `model/providers` contains the production provider set, app
composition resolves them from Layer 4 packages, and model replay preserves
provider-critical metadata.

**Slice 4 Evidence:** The canonical Layer 4 LLM streaming contract is
`model.ResponseEvent`; old `ports/model.StreamEvent` has been removed from the
active rewrite module. Architecture lint now rejects `StreamEvent` references
in new Layer 4 runtime directories (`model`, `agent`, `runner`, `session`).
Shared HTTP client, SSE, status error,
usage, tool-argument, discovery, and OpenAI-compatible provider helpers are in
`model/providers` with focused tests. CodeFree auth now has a Layer 4
store/refresher abstraction that does not read local credential files directly.
Mocked provider tests cover OpenAI streaming, OpenAI-compatible profiles,
Anthropic, Gemini, MiniMax, Ollama, CodeFree JSON and SSE responses, discovery
endpoints, structured output payloads, reasoning metadata, tool calls, usage,
context-overflow status mapping, first-event timeouts, and CodeFree
`retCode=51` retry/exhaustion behavior.

### Slice 5: Compaction Naming, LLM Summarization, And Overflow Retry

**Purpose:** Remove misleading compactor behavior and provide real long-session
quality before production switch.

**Files:**

- Modify: `runner/compactor.go`
- Modify: `runner/compact.go`
- Modify: `runner/runner.go`
- Modify: `runner/compact_test.go`
- Modify: `runner/runner_test.go`
- Modify: `session/event.go`
- Modify: `session/projection.go`

**Steps:**

- [x] Rename the current `LLMCompactor` to `HeuristicCompactor` if it remains
  truncation-only.
- [x] Add a real `LLMSummarizingCompactor` that calls `model.LLM` with a
  summarization request and creates a durable compaction event containing the
  generated summary text, source event/message boundaries, and token estimate.
- [x] Keep a fallback heuristic compactor for tests and providers that cannot
  summarize.
- [x] Ensure overflow retry compacts the prior model context and retries the
  same turn without duplicating user, assistant, or tool events.
- [x] Persist compaction retained model messages so reload rebuilds the same
  summary plus kept tool/user context that the runtime retried with.
- [x] Remove or wire `CompactEvents`; do not keep metadata-only dead code.
- [x] Add tests proving the LLM compactor calls the fake LLM, persists the
  summary event, and uses the summary in rebuilt model context.
- [x] Add tests for overflow retry with tool calls before and after compaction.
- [x] Run `go test ./runner/... ./session/...`.
- [x] Run `git diff --check`.

**Acceptance:** Compactor names match behavior, real LLM summarization exists
behind an interface, and overflow recovery remains replay-safe.

### Slice 6: Streaming Delta Events From LLM Agent To ACP/TUI Projection

**Purpose:** Restore live assistant/reasoning deltas without polluting durable
canonical replay.

**Files:**

- Modify: `agent/llmagent/*`
- Modify: `runner/runner.go`
- Modify: `session/event.go`
- Modify: `acp/*`
- Modify: `protocol/acp/projector/*`
- Modify: retained Layer 3 projection placeholders once upper layers are
  rewritten
- Modify: `test/e2e/layer4/e2e_test.go`

**Steps:**

- [x] Emit `VisibilityUIOnly` assistant message chunks for text deltas.
- [x] Emit `VisibilityUIOnly` reasoning/thought chunks separately from final
  canonical assistant text.
- [x] Ensure durable persistence still stores one complete canonical assistant
  event at the end of the model turn.
- [x] Project transient chunks to ACP `session/update` message chunks with
  `final=false`.
- [x] Ensure replay skips `VisibilityUIOnly` chunks and does not duplicate final
  assistant text.
- [x] Add unit tests for text, reasoning, and mixed tool-call streaming.
- [x] Add Layer 4 E2E asserting transient deltas precede the final canonical
  assistant event.
- [x] Run `go test ./agent/... ./runner/... ./acp ./protocol/acp/... ./test/e2e/layer4`.
- [x] Run `git diff --check`.

**Acceptance:** Layer 4 produces live deltas for presentation while canonical
events remain complete and replayable.

### Slice 7: ACP Subagent And Auto-Review Infrastructure Parity

**Purpose:** Move remaining production infrastructure that Gateway will need
without making Gateway own runtime internals.

**Files:**

- Migrate from: `impl/agent/acp/subagent/*`
- Migrate from: `impl/approval/agentreview/*`
- Create/modify: `agent/approval/autoreview/*`
- Modify: `acp/normalize.go`
- Create/modify: `agent/remote/*`
- Modify: `protocol/acp/client/*`
- Create/modify: `protocol/acp/terminal/*`
- Modify: `protocol/acp/transport/stdio/*`
- Modify: `agent/*` or create `agent/remote/`
- Modify: `runner/spawn.go`
- Modify: `app/runtime.go`
- Test: `protocol/acp/...`
- Test: `agent/...`
- Test: `runner/...`

**Steps:**

- [x] Port ACP client/subagent transport so external ACP agents can be used as
  delegation targets through a narrow `agent.Agent` or `agent.Delegator`
  adapter.
- [x] Normalize external ACP updates into canonical `session.Event` before
  persistence.
- [x] Preserve terminal and permission transport behavior in
  `protocol/acp/client` and `protocol/acp/terminal`.
- [x] Move the auto-review implementation to a package that can be injected as
  `agent.ApprovalRequester` without depending on TUI or old runtime types.
- [x] Add tests for ACP subagent stream merge, permission request/response, and
  cancellation.
- [x] Add tests for auto-review stable transcript delta behavior after the
  package move.
- [x] Run `go test ./agent/... ./acp ./protocol/acp/...`.
- [x] Run `go test -race ./agent/remote`.
- [x] Run `go test -race ./agent/approval/autoreview`.
- [x] Run `go test ./protocol/acp/... ./agent/... ./runner/... ./app/...`.
- [x] Run `git diff --check`.

**Current evidence:** `agent/remote` now exposes an ACP-backed `agent.Agent`
adapter with injectable process/client factory, normalizes streamed ACP
`session/update` payloads through `acp.NormalizeExternalUpdateJSON`, preserves
remote ACP session metadata, treats `final=false` chunks as `VisibilityUIOnly`,
bridges ACP `request_permission` to `agent.ApprovalRequester`, explicitly
sends ACP `session/cancel` when the parent invocation context is cancelled, and
loads an existing external ACP session for continuation instead of creating a
new remote session for the same local child session. It also adds
`agent/approval/autoreview` as a model-backed `agent.ApprovalRequester`
with structured JSON decisions, volatile-id stripping, and runner policy-wrapper
coverage. `agent.ApprovalRequest` now carries a canonical session transcript,
runner injects prior session events into approval requests, and auto-review
maintains a per-session transcript cursor so subsequent reviews use stable
transcript deltas. `protocol/acp/terminal.LocalTerminalAdapter` now owns local
terminal stream bridging with cumulative output reads, no-output suppression,
wait/kill/release control, and resolved terminal refs; the old impl adapter is
an alias to the Layer 4 protocol implementation.
Remaining work: none in Slice 7; continue with Slice 8 file store durability
and cross-process lock audit.

**Acceptance:** External ACP agents and auto-review approval are available to
Layer 3 composition through Layer 4-compatible contracts.

### Slice 8: File Store Durability And Cross-Process Lock Audit

**Purpose:** Ensure the new `session/file` store is safe enough for production
using the old `main` implementation as a behavioral reference.

**Files:**

- Compare: `impl/session/file/*`
- Modify: `session/file/service.go`
- Modify: `session/file/service_test.go`
- Create: `session/file/store_lock_{unix,windows}.go`
- Create: `session/file/syncdir_{unix,windows}.go`
- No change needed: `session/service.go`
- No change needed: `session/validate.go`

**Steps:**

- [x] Audit old `impl/session/file` OS locking behavior against new
  `session/file`.
- [x] Add OS file locks to new `session/file` if missing.
- [x] Add durable temp-file rename, file fsync, and directory fsync for session
  metadata, structured state, and event log writes.
- [x] Ensure appending events validates canonical event shape before writing.
- [x] Ensure reading corrupt JSONL returns an error instead of silently dropping
  model-visible context.
- [x] Add tests for concurrent append, lock contention, and corrupt log reads.
- [x] Run `go test ./session/... ./session/file/...`.
- [x] Run `git diff --check`.

**Slice 8 Evidence:** `session/file` now uses a per-root in-process lock plus
an OS lock file (`.sessions.lock`) for shared reads and exclusive writes,
matching the durable behavior pattern from old `impl/session/file` without
switching any production composition path. Metadata and structured state writes
use temp files, fsync, rename, chmod, and directory fsync; event appends check
short writes, fsync the event log, close explicitly, and fsync the parent
directory. `AppendEvent` continues to canonicalize and validate events before
persisting and now returns timestamp update write failures instead of ignoring
them. Added regression coverage for root lock contention across service
instances, concurrent append across instances, and corrupt JSONL replay errors.

**Acceptance:** New session persistence is durable under multi-process access
and safe for model-context replay.

### Slice 9: Runner Package Hygiene After Behavior Stabilizes

**Purpose:** Reduce maintenance risk without mixing refactors into behavioral
migration slices.

**Files:**

- Modify: `runner/runner.go`
- Modify: `runner/toolwrap.go`
- Modify: `runner/task.go`
- Modify: `runner/spawn.go`
- Create: focused `runner/*.go` splits
- Modify: existing runner tests only as needed

**Steps:**

- [x] Split sandbox/tool preparation out of `runner.Run`.
- [x] Split compaction and overflow retry orchestration out of `runner.Run`.
- [x] Split task/spawn augmentation into focused files behind narrow private
  interfaces.
- [x] Keep public `runner.Config`, `runner.New`, and `runner.Run` stable unless
  tests prove the contract is wrong.
- [x] Run `go test ./runner/...`.
- [x] Run `git diff --check`.

**Slice 9 Evidence:** `runner.Run` now reads as invocation orchestration:
session bootstrap, prior-event snapshot, agent preparation, pre-run
compaction, prompt assembly, user event persistence, hooks, and agent loop
dispatch. Sandbox routing, tool context construction, MCP/tool resolution,
task manager creation, spawn delegation, tool wrapping, and tool catalog
construction live in `runner/invocation_prepare.go`. Agent execution,
transient/persistent event handling, observer draining, and single overflow
retry dispatch live in `runner/invocation_run.go`. Proactive compaction moved
behind `compactBeforeInvocation` in `runner/compact.go`, sharing the same
compaction payload semantics as overflow recovery. Public `runner.Config`,
`runner.New`, and `runner.Run` signatures remain stable.

**Acceptance:** Runner production code is easier to review and does not change
observable semantics.

## Execution Strategy

Run slices in the listed order unless a production bug requires a focused
detour. Slices 3 and 4 can run in parallel after Slice 1 if separate workers
own sandbox and provider migration and merge through `app/runtime.go`
carefully. Slices 5 and 6 should not run before Slice 4 finalizes the stream
event shape, because provider replay metadata and streamed deltas are coupled.

Recommended PR grouping:

1. Boundary/lint fix: Slice 1.
2. Cancellation/spawn correctness: Slice 2.
3. Sandbox migration: Slice 3.
4. Provider migration: Slice 4.
5. Compaction and streaming: Slices 5 and 6, either separate or in one
   coordinated PR if the stream event contract changes.
6. ACP subagent and auto-review: Slice 7.
7. File-store durability: Slice 8.
8. Runner hygiene: Slice 9.

## Gate Before Layer 3 Production Switch

Layer 3 scaffolding may proceed now. Direct production switching is out of
scope for this branch stage; replacing the old production runtime should
require:

- `runner` has no `tool/builtin/*`, `impl/*`, ACP, Gateway, TUI, or `app`
  imports.
- App composition uses `model/providers` and `sandbox/*`, not
  `impl/model/providers` or `impl/sandbox`.
- `go test ./session/... ./model/... ./sandbox/... ./tool/... ./agent/... ./runner/... ./protocol/acp/... ./app/...` passes.
- `go test ./test/e2e/layer4` includes replay, real provider smoke, sandbox
  routing, spawn cancel/restart, plugin MCP loopback, streaming deltas, and
  compaction overflow retry.
- `git diff --check` passes.
- Architecture lint is part of `make quality`.

## Active Rewrite Branch Cleanup

The active rewrite branch keeps Layer 4 and upper-layer placeholders in one
module and deletes the old production roots that used to compete with the new
architecture:

- Deleted active roots: `impl/`, `surfaces/`, `tui/`, `headless/`, `eval/`,
  `cmd/caelis/`, `app/gatewayapp/`, `internal/kernel/`, `internal/cli/`,
  `internal/acpe2eagent/`, `internal/evalharness`, `internal/bootstrap`,
  `internal/modelcataloggen`, and `ports/`.
- Retained placeholders: `app/`, `app/commands/`, `gateway/`, and
  `gateway/kernel/`.
- Reference source for old behavior: `../caelis-main-reference` checked out
  from `main`.

Do not reintroduce imports from the deleted roots. If old behavior needs to be
consulted, compare against the reference worktree or `git show main:<path>` and
port the required semantics into the current Layer 4 package.

## Explicit Non-Goals

- Do not move Gateway turn registry, live subscription fanout, or UI state into
  Layer 4.
- Do not add broad legacy replay guesses before `v1.0.0`; normalize only
  external ACP/imported events at the boundary.
- Do not preserve the `ports/impl` split for newly polished infrastructure.
- Do not reintroduce old production roots into the active rewrite module; old
  code is reference material from `main`, not an import target.
