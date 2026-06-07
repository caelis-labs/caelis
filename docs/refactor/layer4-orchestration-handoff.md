# Layer4 Orchestration Core — Handoff Document

## Goal

Create a clean Layer4 orchestration core that carries mainline Caelis multi-Agent behavior:
- Unified ACP-based SPAWN, including self/internal agents
- External ACP sidecar and delegated participants
- Durable child/participant handles
- Context cursor synchronization
- Correct main/side/delegated visibility
- Replay-safe persistence
- Low duplication and clear interfaces

**Architecture decision:** Layer4 owns all Agent scheduling, runtime execution, context synchronization, multi-Agent orchestration, ACP child/side-agent coordination, SPAWN/TASK lifecycle, approval bridging, and durable orchestration state. Gateway is Layer3 composition/configuration only.

## Completed Work

### Step 1: Architecture Docs & Lint ✅

- `docs/refactor/architecture-boundaries.md` — added `orchestrator/` to Layer4 package tree, dependency rules table, domain responsibility map, cross-package import diagram
- `AGENTS.md` — replaced old `ports/internal/kernel` references with new orchestrator/acp/gateway boundaries
- `scripts/arch_lint.go` — added rules:
  - `orchestrator/` may import `acp`, `agent`, `runner`, `session`, `tool`, `policy`
  - `orchestrator/` must NOT import `app/`, `gateway/`, `tui/`, `headless/`, `cmd/`, `protocol/acp/`
  - `runner/` must NOT import `orchestrator/` (use injected interfaces)
  - New code must NOT import `protocol/acp/` (migration target)

### Step 2: ACP Package Consolidation ✅

`acp/` is the single canonical ACP package. `protocol/acp/` is deprecated.

- `acp/client/types.go` — new file with:
  - `UpdateEnvelope`, `PermissionHandler`, `ACPClient`, `ACPReusableClient` interfaces
  - `ACPClientFactory`, `ACPClientCallbacks`, `ProcessFactory`, `ProcessFactoryConfig`
  - `ProcessFactory` wrapping `acp/client.Client` for process-backed ACP agents
  - Compatibility type aliases: `InitializeResponse`, `NewSessionResponse`, `PromptResponse`, `LoadSessionResponse`, `ToolCallUpdate`, `PermissionOption`, etc.
  - `PermissionSelectedOutcome`, `SelectPermissionOptionID` helpers
- `acp/client/client.go` — added `PromptText(ctx, sessionID, text)` convenience method
- `agent/remote/acp.go` — migrated from `protocol/acp/client` to `acp/client`:
  - `ACPClient`, `ACPReusableClient`, `ACPClientCallbacks`, `ACPClientFactory`, `ProcessFactory` are now type aliases to `acp/client`
  - `toolCallName` updated to use `acp.ToolCallUpdate` (non-pointer fields)
  - All method calls updated to use `acp.NewRequest` patterns
- `agent/remote/acp_test.go` — updated to use `acp` types directly

### Step 3: Orchestrator Package ✅

New package: `orchestrator/`

| File | Purpose |
|---|---|
| `orchestrator/doc.go` | Package docs |
| `orchestrator/orchestrator.go` | `Orchestrator` struct, `Config`, `New()` |
| `orchestrator/registry.go` | `Registry` interface, `MemoryRegistry`, `AgentConfig`, `ExternalAgentConfig`, agent resolution |
| `orchestrator/child.go` | `ChildHandle`, `Anchor`, `DelegationState`, done channel, cancel, wait |
| `orchestrator/delegator.go` | `SpawnDelegator` impl: `Spawn`, `Wait`, `Continue`, `Cancel`, task snapshot persistence |
| `orchestrator/context_view.go` | `MainContext`, `SidecarContext`, `DelegatedChildContext`, `ParentVisibleSummary` |
| `orchestrator/permission.go` | `BridgePermission`, `BuildChildPermissionRequest` |
| `orchestrator/stream.go` | `StreamFrame`, `StreamSink`, `ConvertACPUpdateToFrame`, `FinalFrame`, `ErrorFrame` |
| `orchestrator/orchestrator_test.go` | 4 tests: spawn, cancel, context view exclusion, context view shareable |

### Step 4: Runner Integration ✅

- `runner.Config.SpawnDelegator` already supports injection — orchestrator's delegator is used when set
- Orchestrator's `runInternalChild` creates a child `runner.Runner` and saves task snapshots to `TaskStore`
- TASK tool reads from the same `TaskStore` — SPAWN → TASK flow works end-to-end
- `runner/spawn.go` internal delegator remains as fallback when no orchestrator is provided

### Step 5: Session Schema Extensions ✅

- `session/types.go`:
  - `ControllerBinding` — added `Kind`, `Label`, `RemoteACPSessionID`, `ParentTurnID`, `DelegationID`, `ContextSyncSeq`, `Source`, `Metadata`
  - `ParticipantBinding` — added `Role`, `Kind`, `AgentName`, `Label`, `RemoteACPSessionID`, `ParentTurnID`, `DelegationID`, `ContextSyncSeq`, `Source`, `CreatedAt`
  - Added `ControllerKind`, `ParticipantRole`, `ParticipantKind` enums
- `session/event.go`:
  - `ActorRef` — added `ParticipantKind`, `DelegationID`
  - `Scope` — added `ControllerKind`, `ParticipantRole`, `RemoteACPSessionID`
  - Added context cursor state keys: `StateKeyParentLastEvent`, `StateKeyControllerEpoch`, `StateKeyParticipantCursor`, `StateKeyACPRemoteSessionID`, `StateKeyACPRemoteCursor`

### Step 6: Context View Builders ✅

Implemented in `orchestrator/context_view.go` with tests:
- `MainContext(events)` — includes canonical main events, shareable sidecar events, SPAWN anchor/result; excludes delegated child transcript
- `SidecarContext(events, participantID)` — main events + participant's own events
- `DelegatedChildContext(events, delegationID)` — child's own transcript
- `ParentVisibleSummary(events, delegationID)` — SPAWN anchor + final result only

## Remaining Work

### Step 7: Unify Internal Self SPAWN Through ACP 🔄

**Status:** Foundation in place, ACP loopback adapter not yet built.

**What exists:**
- Orchestrator runs internal agents via `runner.Runner` (direct path)
- `acp.Handler` has `Loopback` in-memory transport for testing

**What's needed:**
- `orchestrator/loopback.go` — wrap `agent.Agent` as `acp.Agent` (implements Initialize, NewSession, Prompt, Cancel)
- Use `acp.Handler.Serve` with `acp.Loopback` in-process transport
- Internal child agents go through same ACP client/session/update path as external
- Environment variable `SDK_ACP_ENABLE_SPAWN=0` to prevent recursive spawning
- Remove direct "runner recursively creates child Runner" path once ACP loopback is tested

### Step 8: Durable ACP Child Continuation 🔄

**Status:** Child handles exist in memory; persistence not wired.

**What exists:**
- `ChildHandle` with `Anchor` (task ID, child session ref, remote ACP session ID, agent name)
- `TaskStore` persistence for task snapshots

**What's needed:**
- Persist `Anchor` fields in session structured state (`StateKeyACPRemoteSessionID`, etc.)
- On continuation after process restart, load remote ACP session via `client.LoadSession`
- File-backed store restart tests

### Step 9: Stream Merge and Projection 🔄

**Status:** Frame types defined; callback pipeline not wired.

**What exists:**
- `StreamFrame`, `StreamSink`, frame constructors in `orchestrator/stream.go`

**What's needed:**
- Wire ACP update callbacks from child → `StreamSink.PublishStream`
- Parent SPAWN receives tool_call_update stream/final summary
- Suppress parent SPAWN echo from child events
- `agent_thought` chunks should not pollute parent tool output
- Transient live frames non-persisted unless final canonical
- Tests for running text, structured tool event, final closed frame, cancelled/failed frame

### Step 10: Permission Bridge 🔄

**Status:** Bridge logic implemented; ACP callback integration not wired.

**What exists:**
- `BridgePermission` in `orchestrator/permission.go` — maps child ACP request → parent approval → outcome
- `BuildChildPermissionRequest` constructs request from ACP + handle context

**What's needed:**
- Wire ACP `session/request_permission` callback through `BridgePermission`
- Parent session ref, parent call ID, task/delegation ID, child participant ID, remote ACP session ID carried through
- Response maps back to ACP selected option
- Tests for allow/reject/cancel

### Step 11: Gateway Integration 🔄

**Status:** Not started.

**What's needed:**
- `app/runtime.go` — wire `orchestrator.Orchestrator` into `Runtime`
- `app/gateway.go` — gateway calls orchestrator for multi-agent operations
- Gateway wraps orchestrator handles, does NOT own turn lifecycle or agent semantics
- Existing e2e tests continue to pass

## Verification

After each remaining step:
```bash
go test ./session/... ./agent/... ./runner/... ./acp/... ./orchestrator/... ./app/... ./test/e2e/layer4
go run scripts/arch_lint.go
git diff --check
```

## Dependency Graph (implemented)

```
app/ ──→ orchestrator/ ──→ acp/ (canonical), agent/, runner/, session/, tool/, policy/
  \                        
   \──→ gateway/       

runner/ uses orchestrator via injected SpawnDelegator (no import)
tool/builtin/spawn/ uses agent.SpawnDelegator (unchanged)
```

## Key Design Decisions

1. **`acp/` is canonical** — `protocol/acp/` is deprecated, being absorbed
2. **`orchestrator/` owns multi-agent orchestration** — not Gateway, not runner
3. **Runner uses injected SpawnDelegator** — orchestrator provides it, runner doesn't import orchestrator
4. **Task snapshots in TaskStore** — SPAWN saves, TASK reads, unified flow
5. **Context views are pure functions** — `MainContext(events)` returns filtered events
6. **Session schema extended, not replaced** — backward-compatible additions to ControllerBinding, ParticipantBinding, ActorRef, Scope
