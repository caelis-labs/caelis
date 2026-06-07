# Rewrite Roadmap

The rewrite is delivered as a sequence of phases. It is not an incremental
cleanup of the current packages. Each phase creates a new, smaller architecture
surface and validates it before the next layer depends on it.

## Principles

1. **Architecture before code.** Phase 1 defines package boundaries,
   interfaces, built-in layering, and validation gates. It does not implement
   behavior.
2. **Domain contracts before built-ins.** Root domain packages must be clear
   before nearby built-in implementations are added.
3. **Inside-out delivery.** Session, model, tool, sandbox, and policy come
   before agents, runner, gateway, ACP, TUI, and app wiring.
4. **Gateway is the surface boundary.** TUI, headless, and ACP server consume
   gateway events; they do not call runner internals directly.
5. **Composition does not own presentation.** `app/` constructs the shared
   control/runtime graph. `cmd/caelis` selects and starts the Presentation
   package.
6. **Delete old code only after replacement is proven.** Current code remains
   the behavioral reference until each replacement phase passes its tests.

## Phase 1: Architecture And Interface Design

**Goal:** Design the complete architecture before behavior implementation.

### 1A: Documented Architecture

Deliverables:

- final package tree;
- dependency rules and architecture-lint rules;
- domain responsibility map;
- interface sketches for each domain;
- built-in implementation layering from
  [builtin-layering.md](builtin-layering.md);
- session/runtime/ACP invariants from
  [session-runtime-contract.md](session-runtime-contract.md);
- Phase 1 preflight checklist from
  [phase-1-preflight.md](phase-1-preflight.md);
- validation gates from [validation-plan.md](validation-plan.md).

No Go behavior is implemented in 1A.

### 1B: Optional Contract Skeleton

If a code skeleton is created before implementation, it is still behaviorless:

- `doc.go` files;
- domain interface and type definitions;
- compile-only test fixtures or mocks;
- architecture-lint configuration;
- no file store, provider, tool, sandbox, runner, gateway, ACP, or TUI
  behavior.

Do not add production paths that panic only because implementation is deferred.
If a constructor cannot be meaningful without behavior, leave it out until that
phase.

### Exit Criteria

- Architecture docs agree with each other.
- Package tree and dependency rules form a strict DAG.
- Built-in ownership is decided before built-ins are written.
- No provider-specific API appears in public Caelis contracts.
- Optional skeleton, if created, compiles and passes architecture lint.

## Phase 2: Core Domain Contracts

**Goal:** Create the small root domain packages and pure value types.

Packages:

- `model/`: provider-neutral messages, parts, tool specs, output specs,
  stream events, request/response types, model refs, and registry interfaces.
- `session/`: session identity, state, durable events, visibility, scope,
  participants, lifecycle, and model-context reconstruction contract.
- `tool/`: tool declaration, schema, call, result, registry, observer, errors,
  and truncation contracts.
- `sandbox/`: backend-neutral command, filesystem, constraints, descriptor,
  status, and factory contracts.
- `policy/`: action, decision, profile, engine, mode options, metadata keys.
- `agent/`: agent interface, context hierarchy, run config, callbacks.

Exit criteria:

- root domain packages compile;
- no concrete built-in behavior exists yet;
- tests cover clone/normalize/validate helpers that are pure contract logic;
- architecture lint proves root domains do not import runner, gateway, ACP,
  TUI, app, or concrete providers.

## Phase 3: Persistence, Providers, Sandbox, And Policy

**Goal:** Implement foundational backends that later layers need.

Work:

- `session.InMemoryService()` and `session/file/`;
- `model/providers/` and `model/catalog/`;
- `sandbox/host/`, `sandbox/darwin/`, `sandbox/linux/`, `sandbox/windows/`;
- `policy/presets/`;
- optional `artifact/` only if the architecture keeps a separate artifact
  domain.

Exit criteria:

- session store round-trip tests pass;
- model provider tests pass with mocked HTTP/SSE;
- sandbox backend tests pass on supported platforms;
- policy decision tests pass;
- no gateway, ACP, or TUI package is required to test these backends.

## Phase 4: Built-In Tools And Skills

**Goal:** Implement built-ins near their domain interfaces without creating a
new `impl/` layer.

Work:

- `tool/builtin/filesystem/`: `READ`, `WRITE`, `PATCH`, `LIST`, `GLOB`,
  `SEARCH`;
- `tool/builtin/shell/`: `RUN_COMMAND`;
- `tool/builtin/task/`: `TASK` declaration and narrow task-controller contract;
- `tool/builtin/plan/`: `PLAN`;
- `tool/builtin/spawn/`: `SPAWN` declaration and delegation contract;
- `skill/` and `skill/embedded/`.

Exit criteria:

- each built-in has focused tests;
- built-ins return provider-neutral `tool.Result`;
- built-ins do not emit ACP wire payloads or TUI view models;
- policy and sandbox enforcement remain wrappers supplied by runner/policy.

## Phase 5: Agent And Runner

**Goal:** Implement model-backed agent execution and one invocation runner.

Work:

- `agent/llmagent/`: model request construction, streaming response handling,
  invalid tool-call repair, multi-tool execution, and semantic event emission;
- `agent/workflow/`: defer loop, parallel, and sequential workflow agents until
  a concrete product requirement exists; do not implement them only because the
  reference architecture has workflow specializations;
- `runner/`: session loading, context preparation, tool resolution, policy and
  approval wrappers, compaction recovery, task/subagent execution, event
  persistence, and run state.

Exit criteria:

- agent loop tests pass for streaming, non-streaming, tool calls, and errors;
- runner tests pass for context preparation and durable event persistence;
- compaction overflow recovery does not duplicate user input or tool calls;
- runner does not import gateway, ACP, TUI, app, or concrete provider packages.

## Phase 5X: Layer 4 Gap Closure Before Gateway

**Goal:** Close the remaining Agent SDK gaps before Gateway depends on Layer 4.
This phase is a temporary planning gate created from the parallel Explorer
audit after Phase 5.9 and the follow-up Layer 4 E2E work. It keeps Gateway from
absorbing session, runner, sandbox, policy, task, subagent, or ACP protocol
responsibilities that belong in the infrastructure layer.

### Confirmed Gap Register

The table below records the audited status after comparing the new Layer 4
packages with the old runtime packages. "Confirmed" means the new architecture
does not yet provide equivalent behavior. "Partial" means the contract or a
basic implementation exists, but the capability is not fully wired or tested.

| ID | Area | Status | Evidence And Required Direction |
| --- | --- | --- | --- |
| C1 | policy safety | Confirmed | New `policy/presets` allows most calls except escalated permission. Port the old workspace-write path rules, sensitive-root protection, git hard-deny rules, and per-tool decisions from `impl/policy/presets`. |
| C2 | subagent lifecycle | Confirmed | New `tool/builtin/spawn` has one-shot `Delegator.Spawn`. Add child session lifecycle, wait/continue/cancel, depth limits, approval bridging, and task-backed handles before Gateway. |
| C3 | async task start | Confirmed | New `tool/builtin/task.Controller` can wait/write/cancel but cannot start commands. Restore a `StartCommand` contract and durable task snapshots so `RUN_COMMAND` can yield long-running work. |
| C4 | dynamic tool augmentation | Confirmed | Old runtime injects command/spawn/task wrappers and auto-adds `TASK`. New runner only applies static policy/observer/truncation wrappers. Add runner-owned augmentation before tools are prepared. |
| C5 | controller/participant bindings | Confirmed | New `session.Service` lacks `BindController`, `PutParticipant`, and `RemoveParticipant`. Add these to keep ACP/built-in participants out of Gateway-only state. |
| C6 | durable event validation | Confirmed | New `session/file.AppendEvent` persists events without canonical validation. Add store-boundary validation and fail on invalid event logs instead of silently skipping corrupt context. |
| H1 | model compaction | Partial | New runner has heuristic compaction; old runtime has model-generated summaries and overflow retry. Add a compactor interface, LLM summarizer, and retry loop. |
| H2 | concurrent tool execution | Confirmed | New `llmagent` executes tool calls serially. Preserve ordering while running independent tool calls concurrently and persisting paired results deterministically. |
| H3 | prompt assembly wiring | Partial | `app/prompt` and skill discovery exist, but `runner` and `llmagent` do not consume assembled system prompts. Wire prompt assembly into agent preparation without moving prompt ownership into UI packages. |
| H4 | sandbox routing | Partial | New `sandbox.Factory` exposes available backends and runner picks the first backend. Add route selection, requested backend handling, fail-closed sandbox mode, setup status, and platform fallback diagnostics. |
| H5 | observer wiring | Partial | `tool.Observer` and wrappers exist, but `runner` passes nil. Add a runner observer bridge that emits transient session events and proves they are not persisted. |
| H6 | tool-result multimodal replay | Confirmed | `projectToolResultToModel` concatenates text parts only. Preserve text, JSON, media, file refs, and terminal/file display separation when rebuilding model context. |
| H7 | structured session state | Confirmed | New `session.State` is `map[string]string`; old state supports structured values. Use a structured JSON value contract with clone/replace/snapshot helpers. |
| H8 | event canonicalization | Confirmed | New events are clean v2 only. Add a narrow normalizer for external ACP/imported events, not a broad legacy compatibility layer. |
| H9 | ACP client | Confirmed | New `acp/client` is still a doc stub; old `protocol/acp/client` has JSON-RPC stdio transport, session methods, terminal, filesystem, and permission handlers. Port the contract into the new `acp/client`. |
| H10 | ACP terminal lifecycle | Confirmed | New `acp/terminal` is a doc stub. Port terminal create/output/wait/kill/release types and tests into Layer 4 protocol packages. |
| H11 | ACP permission transport | Partial | New ACP permission types exist, but no client/server transport or projector integration exists in Layer 4. Implement request/response handling before ACP server work. |
| M1 | provider replay metadata | Partial | `ProviderMeta` is stored and projected to `_meta`, but model context reconstruction does not consume provider replay metadata. Preserve thought signatures and provider replay boundaries through model requests. |
| M2 | invalid tool-call repair | Confirmed | Old `canonicalizeAssistantToolCalls` repairs provider output. Add repair/retry logic to `agent/llmagent` with bounded attempts and tests. |
| M3 | file mutation diff | Confirmed | New WRITE/PATCH returns compact status only. Produce display-only diff previews while keeping model-visible result minimal. |
| M4 | truncation quality | Partial | New truncation is fixed middle-cut. Add per-part, JSON-aware, and token-budget-aware truncation with metadata that survives durable replay. |
| M5 | agent profile wiring | Partial | Profile parsing exists in `app/prompt`, but agent construction does not apply selected profiles. Wire profile selection through runtime configuration. |
| M6 | ACP projection completeness | Partial | `session.ProjectToACP` is basic. Add terminal content, tool locations, participant details, lifecycle/handoff fields, and standard schema alignment in `acp/projector`. |
| M7 | session state operations | Confirmed | New store has only `UpdateState`. Add snapshot/replace operations so Gateway and ACP clients do not invent state semantics. |
| M8 | mirror/display tests | Partial | Visibility predicates exist; runner-level tests do not cover mirror exclusion or display-only separation across replay/projector paths. |
| L1-L5 | minor protocol/schema gaps | Accepted for 5X | Track through validation tests, but do not block each commit unless they affect durable replay, safety, or ACP standard projection. |

### Delivery Slices

Each slice follows the same rule as earlier phases: implement, request review,
apply at most two review rounds, commit only after approval, then continue.

#### 5X.1 Session Event Hardening

Files:

- modify `session/event.go`, `session/projection.go`, `session/service.go`;
- create focused helpers in `session/validate.go` and
  `session/canonicalize.go`;
- modify `session/inmemory.go` and `session/file/service.go`;
- add tests in `session/*_test.go` and `session/file/*_test.go`.

Work:

- add `ValidateEvent` for payload-kind matching, canonical/mirror durability,
  tool call/result IDs, compaction payload shape, actor/scope normalization,
  and model-visible payload completeness;
- add `CanonicalizeEvent` for zero-value defaults and external normalized ACP
  inputs, without preserving old `ports/session` compatibility fields;
- make `AppendEvent` reject invalid durable events and make JSONL reads return
  a validation error when a persisted line is malformed;
- extend `session.Service` with controller and participant binding operations;
- replace flat string-only state with structured JSON state helpers:
  `SnapshotState`, `ReplaceState`, and atomic `UpdateState`;
- rebuild tool result model context from all model-visible part kinds, not
  only concatenated text.

Acceptance:

- `go test ./session/...` passes;
- store round-trip tests reject invalid durable events and corrupt JSONL;
- `ModelContextFromEvents` preserves text, JSON, media, file refs, tool-use,
  tool-result, provider replay metadata, and mirror/ui-only filtering;
- controller/participant bindings survive file store restart.

#### 5X.2 Policy And Sandbox Safety

Files:

- modify `policy/types.go`, `policy/presets/profiles.go`,
  `policy/presets/profiles_test.go`;
- modify `runner/toolwrap.go`, `runner/toolctx.go`;
- modify `sandbox/types.go`, `sandbox/backend.go`, and platform factories.

Work:

- port workspace-write semantics from old `impl/policy/presets`: read/write
  roots, sensitive home config roots, explicit escalation metadata, git control
  metadata checks, and hard-deny commands such as non-dry-run clean and hard
  reset;
- make policy decisions carry backend-neutral sandbox constraints into tool
  metadata and `tool.Context`;
- make sandbox route selection honor requested backend, candidates, setup
  status, and fail-closed route requirements;
- make host execution an explicit route with a visible reason, never an
  accidental fallback for sandbox-required calls.

Acceptance:

- `go test ./policy/... ./sandbox/... ./runner/...` passes;
- policy tests cover sensitive roots, outside-root writes, git hard-deny,
  escalation approval, and read-only denial;
- sandbox routing tests prove explicit sandbox requests fail when enforcement
  is unavailable;
- Layer 4 E2E proves `RUN_COMMAND` uses the selected sandbox backend.

#### 5X.3 Runtime Tool Augmentation And Task Lifecycle

Files:

- modify `runner/runner.go`, `runner/toolwrap.go`;
- modify `tool/builtin/shell`, `tool/builtin/task`,
  `tool/builtin/spawn`;
- create `runner/task*.go` for runner-owned task lifecycle if no separate
  root `task/` package is introduced;
- add tests under `runner/` and `test/e2e/layer4/`.

Work:

- add runner-owned tool augmentation before agent preparation:
  `RUN_COMMAND` becomes task-aware, `SPAWN` becomes delegation-aware, and
  `TASK` is injected when command or spawn tools are present;
- extend task control with `StartCommand`, durable snapshots, terminal refs,
  cursors, state transitions, wait/write/cancel, and recovery from file store;
- connect `tool.Observer` to transient `session.Event` values with run/session
  identity and no durable write;
- execute independent tool calls concurrently while preserving deterministic
  event ordering and paired tool call/result IDs;
- add WRITE/PATCH display-only diff previews and keep large terminal output
  under truncation policy.

Acceptance:

- `go test ./tool/... ./runner/... ./test/e2e/layer4` passes;
- long-running command E2E returns a task handle, emits transient terminal
  updates, can be waited, and persists only canonical final tool state;
- `SPAWN` creates a child task/session with parent scope and approval bridge;
- concurrent multi-tool E2E proves no duplicated tool calls or mismatched
  results after replay.

#### 5X.4 Agent Runtime Parity

Files:

- modify `agent/llmagent/agent.go`;
- modify or add `runner/compact*.go`;
- modify `app/prompt` wiring through runtime config without importing
  Presentation packages.

Work:

- add bounded invalid tool-call repair before tool execution;
- wire assembled system prompts, selected agent profiles, skills, and
  environment context into the first model request;
- replace placeholder compaction with a compactor contract that can call an
  LLM, create durable compaction events, and retry after provider overflow;
- preserve current user input, assistant tool-use, and tool results exactly
  once across compaction and retry;
- make cancellation and max-call limits leave durable lifecycle events.

Acceptance:

- `go test ./agent/... ./runner/... ./app/prompt/...` passes;
- E2E proves runtime model requests match durable replay before and after
  compaction;
- overflow retry creates one compaction event and then resumes without
  duplicating the triggering user input;
- invalid tool-call repair either produces a valid canonical call or a
  model-visible tool error with bounded attempts.

#### 5X.5 ACP Protocol Core Parity

Files:

- implement `acp/client`, `acp/terminal`, and `acp/projector`;
- align `acp/types.go` with the standard schema currently under
  `protocol/acp/schema`;
- add normalization helpers for external ACP events into `session.Event`.

Work:

- port JSON-RPC stdio client/session methods from old `protocol/acp/client`;
- port terminal create/output/wait/kill/release and filesystem callbacks;
- implement `request_permission` transport and approval response mapping;
- implement `acp/projector` from canonical session events to standard ACP
  `session/update` and `session/request_permission`, with `_meta.caelis`
  containing display hints only;
- normalize external ACP user/tool/participant events into canonical
  `session.Event` before they are stored.

Acceptance:

- `go test ./acp/...` passes;
- ACP projection golden tests cover user, assistant, reasoning, tool call,
  tool update, permission request, plan, terminal, participant, handoff,
  lifecycle, provider metadata, and Caelis `_meta`;
- ACP client loopback tests cover session lifecycle, prompt, cancel, terminal,
  filesystem, and permission requests;
- model-critical content is always present in canonical event payloads, never
  only in `_meta.caelis`.

#### 5X.6 Layer 4 E2E And Review Gate

Files:

- expand `test/e2e/layer4/e2e_test.go`;
- add focused fixtures under `test/e2e/layer4/testdata` only when stable
  golden payloads are needed.

Work:

- keep deterministic E2E for runner/tool/session/projector wiring;
- keep real-provider smoke using local `~/.caelis/config.json`, but label it
  as provider smoke rather than full behavior coverage;
- add loopback ACP agent E2E through new `acp/client`;
- add restart/recovery E2E for file session store, task store, compaction, and
  participant state.

Acceptance:

- `go test ./test/e2e/layer4 -v -count=1` passes locally;
- tests clearly separate real provider calls from deterministic fakes;
- `go test ./...`, `make arch-lint`, and `git diff --check` pass;
- a reviewer approves the Layer 4 gap closure before Phase 6 begins.

### Gateway Entry Gate

Phase 6 must not start until:

- all 5X acceptance gates pass;
- no Critical or High confirmed Layer 4 gap remains open;
- the E2E suite proves runtime context and durable replay context are
  equivalent across normal turns, tool turns, compaction turns, and restart;
- canonical session events project to standard ACP updates without storing ACP
  wire payloads as the source of truth;
- Layer 4 packages still do not import Gateway, Presentation, `app/gatewayapp`,
  or old `ports/impl/protocol/surfaces` packages.

## Phase 6: Gateway

**Goal:** Implement the surface-facing gateway boundary.

Work:

- `gateway/`: service contract, event envelope, turn handle, replay/subscribe
  request and response types, approval payloads, usage payloads;
- `gateway/kernel/`: active turn registry, conflict/cancel/submit semantics,
  approval routing, session-to-gateway projection, replay after cursor,
  controller/participant orchestration.

Exit criteria:

- gateway session lifecycle tests pass;
- active turn conflict, cancel, submit, and replay tests pass;
- approval events preserve tool identity and origin;
- gateway remains ACP-neutral and TUI-neutral.

## Phase 7: ACP

**Goal:** Implement ACP as a protocol projection over gateway semantics.

Work:

- `acp/`: schema and protocol value types;
- `acp/client/`;
- `acp/projector/`: gateway event to ACP event projection;
- `acp/terminal/`;
- external ACP participant normalization into `session.Event`.

Exit criteria:

- ACP projection tests pass;
- ACP protocol value, client transport, projector, and terminal helper tests
  pass without `acp/server/`;
- live and replay projection preserve semantic ordering;
- ACP `_meta.caelis` is never the only copy of model-critical data.

## Phase 8: TUI, App, CLI, And End-To-End

**Goal:** Rebuild the user surfaces on the stable gateway boundary.

Work:

- `tui/transcript/`;
- `tui/commands/`;
- `tui/input/`;
- `tui/theme/`;
- `tui/tuikit/`;
- `tui/controladapter/`;
- `headless/`;
- `acp/server/`;
- `app/`: control/runtime composition and app-level services;
- `app/commands/`: shared command semantics;
- `cmd/caelis`: flag parsing and mode selection;
- headless one-shot surface over gateway;
- ACP stdio serving over gateway.

Exit criteria:

- TUI golden tests pass;
- command dispatch and completion tests pass;
- headless one-shot works;
- ACP stdio serving works;
- `caelis doctor` and sandbox lifecycle commands work;
- all surfaces consume gateway or ACP event streams, not runner internals.

## Phase 9: Cleanup And Release Readiness

**Goal:** Remove old architecture and prepare release-quality code.

Deliverables:

- remove old `ports/`, `impl/`, `surfaces/`, and `protocol/` paths after
  replacement behavior is proven;
- remove old `internal/kernel/` after `gateway/kernel/` is active;
- update README, docs, Makefile, architecture lint, and release scripts;
- update npm packaging paths if binary layout changes;
- run final quality and release dry-run gates.

Exit criteria:

- no references to old package paths remain outside migration notes;
- `go build ./...` passes;
- `make quality` passes;
- `make arch-lint` passes;
- `git diff --check` passes;
- production Go code is materially smaller than the baseline target of
  119682 lines, with a first release target below 60000 lines.
