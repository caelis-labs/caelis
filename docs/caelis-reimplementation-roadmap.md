# Caelis Reimplementation Architecture Roadmap

Status: long-term reference and refactor roadmap
Last updated: 2026-05-30
Scope: conceptual redesign, package layout, dependency rules, and migration path

## Purpose

This document records the target architecture for a clean Caelis reimplementation
or deep refactor. It is intentionally written from a near-greenfield viewpoint:
reuse high-cohesion assets from the current repository, but do not preserve
compatibility layers, legacy replay guesses, or stacked adapter logic simply
because they exist today.

The goal is to reduce over-design and package sprawl while preserving the core
product idea: Caelis is an ACP-native agent runtime and gateway. It should be
able to orchestrate external ACP agents, expose itself as an ACP server to
clients such as Zed, and share one kernel and extension ecosystem across the
terminal UI and a future peer APP surface.

## Current Findings

The current codebase already points in the right direction with `kernel`,
`ports`, `impl`, `protocol`, `surfaces`, and `app/gatewayapp`. The problem is
that those layers have grown into mirrored contracts and broad glue packages.

Key issues:

- `kernel/` is mostly a public alias facade over `internal/kernel`, which makes
  the public contract inherit the internal implementation shape.
- `ports/*` is split by many nouns, producing many global interfaces that are
  hard to keep minimal and local to their actual consumers.
- `app/gatewayapp` has become a second kernel: config, model registry, sandbox
  routing, prompt assembly, runtime rebuild, ACP agent management, and app
  services all live together.
- `impl/agent/local` directly knows ACP controller and subagent concrete
  implementations, which weakens the idea that built-in agents and external ACP
  agents meet only at the gateway/runtime boundary.
- `surfaces/tui/app` and `surfaces/tui/gatewaydriver` are large enough that UI
  state, driver API, rendering, and product commands are difficult to evolve
  independently.
- `session.Event`, `kernel.Event`, and ACP updates currently form overlapping
  semantic surfaces. The target design should have one canonical event model and
  deterministic projections.

## Pi Agent Research Notes

Pi Agent is useful as a design reference because its core is deliberately small.
Its documented direction is a lightweight harness plus a resource and extension
system, with behavior added through extensions, packages, skills, templates, or
external tools instead of being built into the core runtime.

Relevant official references:

- [Pi documentation](https://pi.dev/docs/latest)
- [Pi usage and design principles](https://pi.dev/docs/latest/usage)
- [Pi extensions](https://pi.dev/docs/latest/extensions)
- [Pi SDK](https://pi.dev/docs/latest/sdk)

The relevant ideas to borrow:

- Keep the core small and focused.
- Treat extensibility as resource contribution and composition, not as special
  cases inside the runtime loop.
- Prefer explicit packages, extensions, and templates over hidden hard-coded
  workflow features.
- Keep optional workflows outside the core when they can be modeled as tools,
  commands, extensions, or external processes.

The important difference:

- Pi can keep MCP, sub-agents, permission workflows, and plan modes outside its
  core. Caelis cannot keep ACP outside the core contract because ACP-native
  operation is the product identity.

Therefore the Caelis target is not "Pi with ACP as a plugin". The target is:

> small ACP-native core runtime + stable public contracts + plugin and adapter
> ecosystem.

## Product Identity

Caelis should be designed around these first principles:

- ACP is a first-class protocol boundary, not an incidental adapter.
- Canonical session events are the durable source of truth.
- ACP updates are projections from canonical events, except for external ACP
  ingress where ACP input is normalized before storage.
- Built-in model-backed agents and external ACP agents are peers at the runtime
  boundary.
- Model providers, sandbox backends, stores, tools, prompts, skills, and UI
  renderers are replaceable contributions.
- TUI and the future APP are peer surfaces. They share kernel contracts, app
  services, event streams, command definitions, plugin registries, and resource
  discovery, but not presentation implementation.

## Non-Goals

- Do not add compatibility branches for every old event or storage shape.
- Do not make UI transcript cache the source of model replay.
- Do not let TUI-specific metadata become model-critical data.
- Do not make every concept a top-level public `ports/*` package.
- Do not require Go `plugin` dynamic loading; it is not portable enough for this
  product. Prefer manifests, registries, bundled contributions, and subprocess
  or RPC-backed extensions.
- Do not share TUI widgets with a future APP surface. Share app services and
  view-model contracts instead.

## Target Layering

```mermaid
flowchart TB
  subgraph Clients["Client Surfaces"]
    TUI["TUI"]
    APP["Future APP"]
    CLI["Headless CLI"]
    ZED["Zed / ACP clients"]
  end

  subgraph App["Application Layer"]
    AppSvc["App services\nsessions, models, agents, sandbox, settings"]
    Registry["Contribution registries\nproviders, tools, stores, sandboxes, agents"]
    Resources["Resource discovery\nplugins, skills, prompts, AGENTS.md"]
  end

  subgraph Core["Small Public Core"]
    RuntimeAPI["runtime.Engine"]
    SessionAPI["session.Store + canonical events"]
    ModelAPI["model.Provider"]
    ToolAPI["tool.Registry"]
    SandboxAPI["sandbox.Runtime"]
    PluginAPI["plugin.Contribution"]
  end

  subgraph Engine["Runtime Orchestration"]
    Gateway["ACP-native gateway"]
    Loop["turn loop\nmodel -> tools -> model"]
    Context["context reconstruction"]
    Approval["approval + policy"]
    Tasks["async tasks"]
  end

  subgraph Protocol["ACP Protocol"]
    ACPServer["ACP server"]
    ACPClient["ACP client"]
    Projector["canonical event <-> ACP projection"]
  end

  subgraph Adapters["Adapters"]
    Providers["model providers"]
    Stores["jsonl / sqlite / memory"]
    Sandboxes["host / seatbelt / bwrap / landlock / windows"]
    Tools["builtin and plugin tools"]
    ExternalACP["external ACP agents"]
  end

  TUI --> AppSvc
  APP --> AppSvc
  CLI --> AppSvc
  ZED --> ACPServer
  ACPServer --> Gateway
  AppSvc --> RuntimeAPI
  AppSvc --> Registry
  Registry --> Providers
  Registry --> Stores
  Registry --> Sandboxes
  Registry --> Tools
  Registry --> ExternalACP
  RuntimeAPI --> Gateway
  Gateway --> Loop
  Gateway --> Context
  Gateway --> Approval
  Gateway --> Tasks
  Gateway --> SessionAPI
  Loop --> ModelAPI
  Loop --> ToolAPI
  ToolAPI --> SandboxAPI
  Gateway --> Projector
  Projector --> ACPClient
  ExternalACP --> ACPClient
```

## Target Package Layout

The exact package names can evolve, but the ownership boundaries should remain
stable.

```text
cmd/caelis/
  main.go

core/
  runtime/      # Engine, Turn, EventEnvelope, cancellation, active turn contracts
  session/      # Session, Event, Store, Cursor, Snapshot, state patches
  model/        # Provider, Request, StreamEvent, Message, ToolCall, usage
  tool/         # Tool, Registry, Definition, Call, Result, display metadata
  sandbox/      # Runtime, Backend, FS, Exec, Constraints, setup status
  plugin/       # Manifest, Contribution, Registry, resource descriptors
  config/       # typed config contracts, no file/env side effects

protocol/acp/
  schema/
  jsonrpc/
  transport/
  client/
  server/
  projector/    # canonical event <-> ACP session/update + request_permission

internal/engine/
  gateway/      # sessions, turns, replay, ACP ingress/egress, active runs
  loop/         # model/tool turn execution
  context/      # prompt and model-context reconstruction
  approval/
  compaction/
  tasks/
  control/      # controller, participant, subagent orchestration contracts

internal/app/
  local/        # default composition root
  services/     # service facade consumed by TUI, APP, CLI, ACP server
  settings/     # env/file config loading
  resources/    # plugin, skill, prompt, AGENTS.md discovery
  registry/     # model/tool/sandbox/store/agent registries

internal/adapters/
  model/
    openai/
    anthropic/
    gemini/
    openrouter/
    ollama/
    codefree/
    volcengine/
  store/
    jsonl/
    sqlite/
    memory/
  sandbox/
    host/
    seatbelt/
    bwrap/
    landlock/
    windows/
  tools/
    filesystem/
    shell/
    plan/
    task/
    spawn/
  acpagent/
    external/   # external ACP process as controller, participant, or subagent

internal/surface/
  tui/
    app/
    driver/
    render/
    viewmodel/
    widgets/
  app/
    api/        # future APP-specific adapter over internal/app/services
    viewmodel/
  headless/
  acpserver/

plugins/
  builtin/      # bundled manifests/resources; no hidden engine logic

eval/
scripts/
npm/
docs/
```

## Dependency Rules

- `core/*` must not import `internal/*`, `protocol/*`, `cmd/*`, or UI packages.
- `protocol/acp/schema`, `jsonrpc`, `transport`, `client`, and `server` should
  stay protocol-only. They should not know the local runtime.
- `protocol/acp/projector` may depend on `core/session` and ACP schema.
- `internal/engine/*` depends on `core/*` and local engine sibling packages. It
  must not import concrete model providers, concrete sandbox backends, concrete
  stores, or UI packages.
- `internal/app/*` is allowed to import adapters and wire them together.
- `internal/adapters/*` implements `core/*` contracts. Adapters must not import
  surfaces.
- `internal/surface/*` depends on `internal/app/services`, `core/*`, and
  protocol clients where needed. Surfaces must not import concrete adapters.
- TUI and future APP are peers. Any shared behavior between them belongs in
  `internal/app/services` or shared view-model contracts, not in TUI packages.

## Core Contracts

The public core should be small enough that it is hard to misuse.

### Runtime

```go
type Engine interface {
    StartSession(context.Context, session.StartRequest) (session.Session, error)
    LoadSession(context.Context, session.Ref) (session.Snapshot, error)
    BeginTurn(context.Context, TurnRequest) (Turn, error)
    Interrupt(context.Context, session.Ref) error
    Replay(context.Context, ReplayRequest) (<-chan EventEnvelope, error)
}
```

The engine owns orchestration. It does not own concrete providers, stores,
sandboxes, tools, or UI rendering.

### Session Store

```go
type Store interface {
    Create(context.Context, StartRequest) (Session, error)
    Load(context.Context, Ref) (Snapshot, error)
    Append(context.Context, Ref, []Event) (Cursor, error)
    Events(context.Context, EventQuery) (EventPage, error)
    UpdateState(context.Context, Ref, StatePatch) error
}
```

JSONL and SQLite should both implement this contract. Runtime logic should not
know which one is in use.

### Model Provider

```go
type Provider interface {
    ID() string
    Models(context.Context) ([]ModelInfo, error)
    Stream(context.Context, Request) (Stream, error)
}
```

Provider-specific request details belong in provider config and metadata, not
in the runtime loop.

### Plugin Contribution

```go
type Contribution interface {
    Manifest() Manifest
    Register(context.Context, Registry) error
}
```

Plugins should contribute resources and implementations:

- model providers
- tools
- sandbox backends
- session stores
- ACP agent descriptors
- prompt fragments
- skills
- UI renderer hints

Plugins should not bypass the engine, session store, approval flow, or ACP
projection contract.

## Canonical Event Model

Caelis should keep one durable event model:

- user content
- assistant text and reasoning
- tool calls and tool results
- provider replay metadata
- approval requests and decisions
- compaction checkpoints
- controller and participant lifecycle events
- task lifecycle anchors
- ACP ingress metadata when external ACP agents participate

ACP updates should be deterministic projections from these canonical events.
External ACP input should be normalized into canonical events before persistence.

Rules:

- Model-visible state must live in canonical event fields, not only `_meta`.
- ACP `_meta` may carry display hints and UI-only details.
- `VisibilityUIOnly` stream chunks are transient and not required for replay.
- Replay must rebuild the same semantic model context produced during live
  execution.
- Store round-trip tests are required for any persistence change.

## ACP-Native Runtime Model

Caelis has two ACP directions:

1. Serve ACP: expose Caelis as an ACP server for clients such as Zed.
2. Consume ACP: run external ACP agents as controllers, participants, or
   subagents.

The target architecture should make both directions first-class:

- ACP server ingress turns client requests into engine requests.
- Engine events are projected into standard ACP `session/update` and
  `request_permission`.
- External ACP agent output is normalized into canonical events.
- Built-in agents and external ACP agents use the same session, approval,
  replay, and task contracts.
- Controller handoff, sidecar participants, and delegated subagents are runtime
  concepts, not TUI-only commands.

## Plugin And Ecosystem Model

The ecosystem should look closer to resource assembly than inheritance.

Recommended plugin shape:

```text
plugin.json
prompts/
skills/
tools/
agents/
models/
sandbox/
renderers/
```

Example contribution classes:

- `model.provider`: registers a provider factory.
- `tool.builtin`: registers a local Go tool or subprocess tool descriptor.
- `sandbox.backend`: registers backend selection and runtime factory.
- `store.backend`: registers `jsonl`, `sqlite`, or remote store implementations.
- `acp.agent`: declares an external ACP command and capabilities.
- `prompt.fragment`: contributes prompt text with explicit priority and scope.
- `skill`: contributes discovered skill metadata.
- `ui.renderer`: contributes optional rendering hints for known tool/event
  kinds. These hints must never be the only durable semantic data.

Plugin loading should be deterministic:

1. read manifests
2. validate schema and declared capabilities
3. register contributions
4. compose runtime registries
5. build engine

## TUI And Future APP

The future APP should be a peer to the TUI, not a wrapper around it.

Shared:

- `internal/app/services`
- command definitions and command handlers
- canonical events
- replay and live event subscriptions
- model/provider registry
- sandbox status and setup workflows
- ACP agent registry
- settings and profiles
- view-model contracts for status, transcript, approvals, task panels, model
  selection, and agent management

Separate:

- rendering
- layout
- input handling
- platform-specific notifications
- local persistence for surface-only preferences

Suggested boundary:

```text
internal/app/services
  SessionService
  TurnService
  ModelService
  AgentService
  SandboxService
  SettingsService
  PluginService

internal/surface/tui
  Bubble Tea implementation

internal/surface/app
  Future APP adapter and view models
```

The common service layer should speak in stable DTOs and event streams. It
should not expose Bubble Tea types, terminal color concepts, or desktop UI
types.

## Reuse Versus Rewrite

Good reuse candidates:

- ACP schema, JSON-RPC, client, server, and projection ideas from
  `protocol/acp`.
- Provider implementation details from `impl/model/providers`.
- Sandbox backend implementation details from `impl/sandbox/*`.
- Built-in tool behavior from `impl/tool/builtin/*`.
- Canonical message and event ideas from `ports/model` and `ports/session`.
- Store round-trip tests and replay validation tests.
- TUI rendering components that are already cohesive, after moving them behind
  surface-local boundaries.

Rewrite or heavily reshape:

- `kernel/` and `internal/kernel` mirrored public/internal split.
- `app/gatewayapp` as a giant composition root.
- Global `ports/*` package taxonomy.
- `impl/agent/local` directly coupling to concrete ACP controller/subagent
  implementations.
- `surfaces/tui/gatewaydriver` as a duplicated product API layer.
- Legacy event compatibility fallbacks and heuristic replay reconstruction.

## Migration Strategy

This can be implemented incrementally, but the target should remain a clean
reimplementation.

### Phase 1: Contract Freeze

- Define `core/session`, `core/model`, `core/tool`, `core/sandbox`,
  `core/runtime`, and `core/plugin`.
- Write architecture lint rules for target dependencies.
- Add store round-trip tests for canonical model context reconstruction.
- Add ACP projection tests from canonical events.

### Phase 2: New Engine Skeleton

- Implement `internal/engine/gateway` against the new core contracts.
- Implement a minimal turn loop with one model provider and one tool registry.
- Implement replay from canonical store only.
- Implement approval and permission flow as engine contracts.

### Phase 3: Adapter Migration

- Move model providers behind `core/model.Provider`.
- Move sandbox backends behind `core/sandbox.Runtime`.
- Move JSONL store behind `core/session.Store`.
- Add SQLite store as a parallel adapter without runtime changes.
- Move built-in tools behind `core/tool.Registry`.

### Phase 4: ACP First-Class Runtime

- Rebuild ACP server surface over the new engine.
- Rebuild external ACP controller, participant, and subagent adapters.
- Normalize ACP ingress into canonical events before storage.
- Ensure TUI, APP, CLI, and ACP clients consume the same event stream.

### Phase 5: Surface Split

- Build `internal/app/services` as the only product API consumed by surfaces.
- Port TUI to the service facade.
- Define future APP view-model contracts next to the service facade.
- Remove any TUI-specific assumptions from runtime and app services.

### Phase 6: Remove Old Stack

- Remove the `kernel` alias facade and old `internal/kernel` stack once the new
  engine is feature-complete.
- Remove compatibility replay guesses.
- Retire duplicated gateway-driver APIs.
- Update README and developer docs to the new layout.

## Current Implementation Checkpoint

The first baseline of this roadmap is now represented by new packages that sit
alongside the old stack without importing it:

- `core/*`: stable contracts for runtime, session, model, tool, sandbox, plugin,
  and config.
- `internal/engine/gateway`, `internal/engine/loop`,
  `internal/engine/approval`, and
  `internal/engine/context`: session lifecycle, canonical event append/replay,
  approval/permission policy, model-context reconstruction from durable events,
  and a minimal model/tool turn loop.
- `internal/engine/control`: external participant runner that invokes an ACP
  agent and appends normalized events into the canonical session store. It now
  also has a controller runner for ACP main-controller prompts, normalizing
  responses into controller-scoped canonical events.
- `internal/app/services`: shared service facade for TUI, future APP, CLI, and
  protocol surfaces.
- `internal/app/settings`: shared product settings document for configured
  models and settings-backed custom external ACP agent descriptors, with
  normalized upsert/list/delete operations independent of the old gatewayapp
  config store.
- `internal/app/agents`: small app-level catalog for registerable built-in
  external ACP agent descriptors. The catalog is data-only and stays separate
  from runtime orchestration and package-install side effects.
- `internal/app/resources`: deterministic discovery baseline for enabled
  `plugin.json` manifests, plugin prompt/skill/ACP-agent/renderer descriptors,
  workspace/global `AGENTS.md`, and skill metadata. Plugin-declared ACP agents
  are normalized with plugin-relative working directories and command paths.
  Manifests can also declare provider/store/sandbox/tool factory aliases using
  `name -> uses` bindings.
- `internal/app/registry`: deterministic `core/plugin.Registry`
  implementation for model provider, store, sandbox, tool, ACP agent, prompt,
  skill, and renderer contributions. The local composition root now resolves
  built-in provider/store/sandbox/tool implementations through this registry
  instead of hard-coded construction switches, and applies manifest-declared
  factory aliases before composing the stack.
- `internal/app/prompt`: app-layer prompt assembler that renders discovered
  prompt fragments, `AGENTS.md`, and skill metadata into provider
  instructions without moving filesystem discovery into the engine.
- `internal/app/viewmodel`: surface-neutral session transcript, pending
  approval, participant, and status DTOs shared by the TUI and future APP,
  including runtime store identity needed by read-only diagnostics.
- `internal/app/local`: local composition root for core provider, store, tools,
  sandbox runtime, and engine wiring. It can now build a configured local stack
  from `core/config` without importing the old `ports` or `kernel` packages.
  It also wires plugin-declared and settings-backed custom ACP agents into the
  shared `AgentService`, and injects the built-in ACP agent catalog for
  service-native registration.
- `internal/adapters/model/openai`: core-native OpenAI-compatible Chat
  Completions provider with tool-call, usage, structured-output, reasoning,
  and provider-profile mapping. It now backs OpenAI-compatible, DeepSeek, and
  OpenRouter, Mimo/Xiaomi, Volcengine, and Volcengine Coding Plan factories in
  the app registry.
- `internal/adapters/model/anthropic`: core-native Anthropic Messages API
  provider with text, image, tool-use/tool-result, reasoning replay signature,
  usage, and model-listing mapping. It now backs Anthropic,
  Anthropic-compatible, and MiniMax factories in the app registry.
- `internal/adapters/model/gemini`: core-native Gemini API provider with
  text/image/file content, tool-call/tool-result mapping, thought-signature
  replay metadata, JSON/schema output, reasoning configuration, usage, and
  model-listing mapping.
- `internal/adapters/model/codefree`: core-native CodeFree chat provider with
  clean Caelis credential loading, CodeFree headers, OpenAI-compatible message
  and tool mapping, JSON output mode, usage, version-endpoint model listing,
  and OAuth credential ensure/refresh helpers.
- `internal/adapters/model/ollama`: core-native Ollama `/api/chat`
  provider with model listing, tool-call mapping, reasoning text, JSON output
  mode, and usage mapping.
- `internal/adapters/store/memory`, `internal/adapters/store/jsonl`, and
  `internal/adapters/store/sqlite`: swappable `core/session.Store` adapters
  for ephemeral and durable local composition.
- `internal/adapters/tools/registry`: deterministic in-memory
  `core/tool.Registry`.
- `internal/adapters/sandbox/host`: core-native host sandbox runtime with async
  command session start/open/read/write/wait/cancel support.
- `internal/adapters/tools/shell`: core-native `run_command` tool using
  `core/sandbox.Runtime`.
- `internal/adapters/tools/task`: core-native wait/write/cancel control for
  yielded sandbox sessions.
- `internal/adapters/acpagent/external`: core-native external ACP client that
  normalizes ACP `session/update` and `session/request_permission` traffic into
  canonical `core/session.Event` values.
- `internal/app/services.AgentService`: shared TUI/APP-facing descriptor,
  registration, removal, and invocation surface for external ACP agents
  contributed by local composition or stored in app settings. Runtime-added
  custom agents are resolved through a narrow invoker factory instead of
  rebuilding service state. Built-in ACP agents are registered by copying their
  catalog descriptors into the same settings-backed external agent contract,
  and invocations can target either participant scope or ACP controller scope.
- `internal/app/services.ModelService`: shared model settings and catalog
  surface for configured models, provider model presets, capability defaults,
  and reasoning-level choices used by TUI/future APP connect flows.
- `surfaces/tui/gatewaydriver.BindAppServices`: service-native TUI `/agent`
  list and dynamic `/<agent> <prompt>` baseline for configured external ACP
  agents, recording participant attach/user/assistant activity as canonical
  core session events. It also routes settings-backed `/agent add custom` and
  `/agent add <builtin>` and `/agent remove` through shared app services. The
  same gateway now records `/agent use <agent|local>` as canonical handoff
  events, rebuilds active controller state from canonical handoff and
  controller-scoped events, and routes subsequent prompts to the active
  external ACP controller with the latest known remote ACP session id.
- `internal/app/services.ResourceService`: shared TUI/APP-facing catalog
  surface for discovered plugins, prompt fragments, skills, ACP agents,
  renderer hints, and `AGENTS.md` prompt resources.
- `internal/app/services.ViewService`: shared TUI/APP-facing projection from
  canonical session snapshots to surface-neutral transcript, approval, and
  participant view models.
- `internal/app/services.SandboxService`: shared sandbox status and lifecycle
  surface. The current migrated baseline exposes core-native sandbox status
  from the composed runtime and treats host setup/fix/reset/clean as explicit
  no-op lifecycle operations instead of routing those commands through the old
  stack.
- `protocol/acp/projector/core`: canonical session event projection to ACP
  updates and permission requests.
- `internal/surface/acpserver`: ACP JSON-RPC server over the new runtime engine.
- `internal/e2e`: new-architecture end-to-end harness that exercises local
  composition, ACP stdio serving, plugin resource loading, registry aliases,
  OpenAI-compatible model requests, host-sandboxed shell tools, SQLite
  persistence, canonical reload, and shared view-model projection.

The current verification path covers:

- local stack -> shared services -> engine -> canonical memory store
- configured local stack -> OpenAI-compatible provider -> JSONL store
- configured local stack -> Anthropic/MiniMax, Gemini, CodeFree, and
  DeepSeek/OpenRouter/Mimo/Volcengine provider profiles -> JSONL store
- core-native CodeFree OAuth ensure/model-selection/refresh -> Caelis
  credential store
- configured local stack -> native Ollama provider -> JSONL store
- configured local stack -> SQLite store -> persisted canonical events after
  reload
- model tool call -> shell tool -> host sandbox -> tool result -> model
  continuation
- configured local stack -> built-in shell tool -> host sandbox -> model
  continuation
- approval-aware tool execution -> canonical pending/decision events ->
  `Turn.Submit` resume
- ACP server -> `session/request_permission` -> permission response -> runtime
  approval submission
- external ACP client -> `session/update` notifications -> canonical user,
  assistant, tool, plan, and approval events
- external ACP client -> ACP `session/request_permission` request -> local
  permission handler -> canonical pending/decision approval events
- local stack -> shared `AgentService.Invoke` -> external ACP process ->
  participant runner -> canonical session events
- enabled plugin manifest `acp_agents` -> shared `AgentService` descriptor and
  invoker -> external ACP subprocess -> canonical participant events
- app settings `acp_agents` -> shared `AgentService` descriptor/register/remove
  -> local-stack dynamic invoker factory -> external ACP subprocess ->
  canonical participant events
- built-in ACP agent catalog -> shared `AgentService.RegisterBuiltin` ->
  settings-backed external ACP descriptor -> TUI `/agent add <builtin>`
  catalog and registration path
- TUI `/agent use <agent|local>` -> canonical `EventHandoff` ->
  app-service control-plane state -> ACP controller-scoped prompt routing
- TUI ACP controller response carrying a remote session id -> canonical
  controller-scoped event -> next TUI prompt reuses that remote id through
  app-service controller invocation
- app-service TUI binding -> configured external ACP agent catalog -> dynamic
  participant prompt -> canonical participant/user/assistant events -> TUI
  participant-scoped event projection
- app-service TUI binding -> `/agent add custom` and `/agent remove` for
  settings-backed custom external ACP agents -> shared settings persistence ->
  refreshed agent catalog
- app-service model catalog -> TUI `/connect` model completion and default
  context/output/reasoning values
- local stack -> enabled plugin manifest + workspace `AGENTS.md` ->
  shared `ResourceService` catalog
- CLI `doctor` and `-doctor` -> new local stack -> shared status/sandbox
  services -> redacted text/JSON diagnostics
- CLI `sandbox setup|fix|reset|clean` with host backend -> new local stack ->
  shared sandbox service -> text/JSON sandbox lifecycle status
- resource discovery -> home/workspace skills + plugin descriptors ->
  deterministic app resource catalog
- resource catalog -> app prompt assembler -> loop instructions -> provider
  request
- app registry -> provider/store/sandbox/tool factories -> local stack
  composition
- Go `plugin.Contribution` -> app registry -> contributed store factory ->
  local stack composition
- enabled plugin manifest factory alias -> app registry -> local stack
  provider/store/sandbox/tool selection
- canonical session snapshot -> shared view model -> transcript, pending
  approvals, and participants for TUI/future APP
- JSONL store round-trip -> canonical events -> rebuilt model context
- SQLite store round-trip -> canonical events -> rebuilt model context
- local stack -> ACP server -> JSON-RPC `session/new` and `session/prompt` ->
  ACP `session/update` notifications -> canonical stored events
- new architecture e2e -> enabled plugin manifest prompt + store alias ->
  SQLite store -> ACP server -> OpenAI-compatible mock provider ->
  `run_command` through host sandbox -> canonical event reload -> shared
  TUI/APP view model
- architecture lint for the new dependency boundaries

## Migration Status Review

Review date: 2026-05-30
Stage: new architecture skeleton is in place; product behavior migration is not
complete.

### Review Outcome

The implemented skeleton is aligned with the target direction:

- New `core/*`, `internal/engine/*`, `internal/app/*`, `internal/adapters/*`,
  `internal/surface/acpserver`, `protocol/acp/projector/core`, and
  `internal/e2e` packages do not import the old `ports/*`, `kernel/*`,
  `impl/*`, `surfaces/*`, or `app/gatewayapp` stack.
- Runtime orchestration is expressed through small contracts and concrete
  adapters. Model provider, store, sandbox, tool, plugin resource, ACP agent,
  and surface concerns are not piled into one package.
- Canonical session events are the durable replay source for the new stack.
  ACP updates are projected from those events, and external ACP ingress is
  normalized into canonical events before persistence.
- TUI and the future APP are represented as peer consumers of shared app
  services and surface-neutral view models, not as wrappers around each other.
- The new e2e path proves the skeleton can run an ACP-native turn through plugin
  resources, registry aliases, SQLite, model/tool continuation, host sandbox
  execution, canonical reload, and shared view projection.

The important constraint:

> This checkpoint is an architecture baseline, not a product replacement.
> Single-shot headless CLI and ACP stdio now enter the new stack, but
> interactive TUI, doctor/config/sandbox commands, rich provider catalog,
> sandbox policy, compaction, durable task runtime, and most agent workflows
> still run on the old stack.

### Target State

The migration is complete only when:

- `cmd/caelis` enters the new `internal/app/services` stack for interactive TUI,
  headless CLI, doctor/status/config flows, and ACP stdio serving.
- TUI and the future APP consume the same service facade, command handlers,
  event streams, settings/profile APIs, and view-model contracts.
- Built-in agents and external ACP agents meet only at the gateway/runtime
  boundary, with canonical event storage for all model-visible state.
- Model providers, sandbox backends, stores, tools, prompts, skills, renderer
  hints, and ACP agents are selected through registries or plugin manifests.
- Reloaded model input is rebuilt from canonical durable events and validated
  against live runtime context for normal turns, tool turns, approvals,
  compaction, subagents, and ACP participants.
- The old `kernel`, `ports`, `impl`, `app/gatewayapp`, and old `surfaces/*`
  runtime paths can be deleted rather than bridged by compatibility layers.

### Completed In This Checkpoint

The completed work is intentionally limited to the reusable skeleton:

- Public core contracts: runtime, session, model, tool, sandbox, plugin, and
  typed config.
- Canonical session stores: memory, JSONL, and SQLite.
- Core-native session listing contract across `core/session.Store`,
  `core/runtime.Engine`, memory/JSONL/SQLite stores, and shared app services,
  with app/user/workspace/CWD/search filters, pagination, event counts, and
  last-event timestamps for future TUI/APP resume views.
- Runtime engine skeleton: session start/load/replay, turn execution,
  cancellation, record-events ingress, approval wait/resume, and model/tool
  continuation.
- Model context reconstruction from canonical events.
- OpenAI-compatible provider adapter sufficient for Chat Completions, tool
  calls, structured output, reasoning content, DeepSeek reasoning defaults, and
  OpenRouter attribution headers. It also covers Mimo/Xiaomi and Volcengine
  thinking payload profiles and provider default endpoints.
- Anthropic Messages API provider adapter sufficient for text/image content,
  tool-use/tool-result mapping, thinking signature replay metadata, usage,
  model listing, and Anthropic-compatible MiniMax auth/default endpoint
  behavior.
- Gemini API provider adapter sufficient for text/image/file content,
  tool-call/tool-result mapping, thought-signature replay metadata, usage,
  model listing, JSON/schema output, and Gemini 2.x budget-based reasoning
  configuration.
- CodeFree provider adapter sufficient for non-stream chat completions,
  CodeFree header/auth semantics, clean Caelis credential loading,
  OpenAI-compatible message/tool mapping, JSON output mode, usage, model
  listing, OAuth credential ensure/refresh, and headless CLI selection.
- Native Ollama provider adapter sufficient for `/api/chat`, model listing,
  tool calls, reasoning text, JSON output mode, and usage mapping.
- Host sandbox adapter, core-native async command sessions, core-native
  `run_command` tool, core-native `task` wait/write/cancel control, and
  core-native filesystem tools: `read_file`, `list_directory`, `glob_files`,
  `search_files`, `write_file`, and `patch_file`.
- Core-native `update_plan` tool with runtime conversion into canonical
  `session.EventPlan` events.
- App composition root, registry, plugin manifest discovery, prompt assembly,
  resource catalog, external ACP agent descriptors, and shared services.
- App settings and model-selection baseline: clean `internal/app/settings`
  document/store/manager, token redaction by default, provider profiles,
  generated model aliases/ids, default model selection, model delete, session
  model override state, runtime model-profile projection, and request-time model
  routing through app registries.
- App model catalog baseline: shared `ModelService` exposes configured
  provider models, built-in provider model presets, capability defaults, and
  reasoning levels so TUI and future APP connect/setup flows do not need to own
  provider capability tables.
- App session mode baseline: shared app services persist a per-session approval
  preset, ACP exposes it through `session/set_mode` and the `mode` config
  option, and the core approval policy receives the selected mode for each tool
  review.
- Headless surface baseline: `internal/surface/headless` runs one-shot prompts
  over shared app services, starts or resumes canonical sessions through the
  engine, resolves approvals with explicit policy hooks, renders text/JSON
  results, and is covered by a new local-stack e2e path using settings model
  routing, the OpenAI-compatible adapter, host sandbox tools, and canonical
  persistence.
- Production CLI baseline for headless and ACP stdio: `internal/cli` now routes
  single-shot prompts and the `caelis acp` subcommand through the new
  `internal/app/local` service stack and core-native surfaces.
- Shared TUI/APP view-model projection for transcript, current plan, pending
  approvals, participants, and runtime/session/model/mode/agent/resource status,
  including store identity for read-only diagnostic displays.
- Core-native ACP server for initialize, session/new, session/prompt,
  session/list, session/load, session/resume, session/close, cancel,
  `session/update`, and permission request bridging. It also exposes configured
  model metadata and model/reasoning selection through ACP session model/config
  methods when the shared app settings service is available, and applies those
  session overrides to subsequent ACP prompts through the shared app turn
  service. It also exposes core-native session modes and the non-model `mode`
  config option through shared app services. `session/load` replays canonical
  stored events through the same ACP projection path used for live updates, and
  `session/close` interrupts any active turn while remaining idempotent when no
  turn is running.
- Core-native external ACP process adapter for participant-style invocation and
  normalized canonical event recording.
- Service-native TUI `/agent list` and dynamic `/<agent> <prompt>` baseline
  for configured external ACP agents, with participant attach/user/assistant
  activity recorded as canonical session events and projected back through the
  existing TUI driver event stream.
- Service-native settings-backed custom external ACP agent registration and
  removal, including TUI `/agent add custom` and `/agent remove` for custom
  agents without rebuilding the app-service stack.
- Service-native built-in ACP agent catalog and non-install registration,
  including TUI `/agent add <builtin>` completion/registration backed by the
  same settings document used for external ACP descriptors.
- Service-native `/agent use <agent|local>` baseline for registered external
  ACP agents, using canonical handoff events and controller-scoped ACP prompt
  execution through shared app services.
- Architecture lint rules for the new package boundaries.
- End-to-end skeleton test covering plugin resources, SQLite, ACP server,
  OpenAI-compatible provider mock, shell tool execution, canonical reload, and
  shared view projection.

### Not Yet Migrated

These are product capabilities still owned by the old implementation and must
be migrated before retiring the old stack:

1. CLI and process entrypoints
   - Migrated baseline: single-shot headless prompts and `caelis acp` now build
     the new `internal/app/local` stack directly and use core-native headless
     and ACP server surfaces.
   - Migrated baseline: the production interactive TUI entrypoint now builds
     the same `internal/app/local` stack and injects `internal/app/services`
     into the existing TUI driver through `BindAppServices`, so normal TUI
     prompts, status/model/mode state, and core turn streaming no longer
     construct `app/gatewayapp`.
   - Migrated baseline: the production interactive TUI `/doctor` read-only
     status path can now render provider/model, session, store, sandbox, and
     active-job diagnostics from `internal/app/services.Status().View()`
     through the shared app status view, instead of requiring
     `app/gatewayapp` doctor state.
   - Migrated baseline: standalone CLI `doctor`/`-doctor` and
     `sandbox setup|fix|reset|clean` now build the new
     `internal/app/local` stack and use shared app status/sandbox services.
     Host sandbox lifecycle commands are explicit no-ops with status output,
     not old-stack fallbacks.
   - Still pending: default home layout, full config hydration, rich setup
     diagnostics, non-host sandbox repair/setup flows, and several command
     dispatch paths still depend on `app/gatewayapp` and `kernel.Service`.

2. TUI surface
   - Migrated baseline: `surfaces/tui/gatewaydriver` can now project
     `internal/app/viewmodel.StatusView` into the existing TUI
     `StatusSnapshot` through an injected app-status view function. This
     creates a narrow service-native status path for the current TUI shell
     without importing `gatewayapp` into the status projection.
   - Migrated baseline: `surfaces/tui/gatewaydriver.BindAppServices` can bind
     the existing TUI driver control points for session start, status,
     model listing, model selection, and session mode set/cycle directly to
     `internal/app/services`. This gives `/status`, `/model`, and `/approval`
     a service-native driver path before the full interactive TUI entrypoint
     moves off the old stack.
   - Migrated baseline: the same binding now supplies a thin core-runtime
     `GatewayService` adapter for TUI submit, active-turn submission,
     interrupt, replay, session list/resume, and minimal control-plane state.
     Basic interactive prompts can therefore enter `internal/app/services`
     without constructing `app/gatewayapp`, while unsupported advanced
     participant operations fail explicitly at the adapter boundary.
   - Migrated baseline: `internal/cli` now wires the production interactive
     TUI to this app-service binding for the core-native host runtime path,
     and the binding includes app settings backed model connect/delete/use
     operations.
   - Migrated baseline: `/compact` now has an app-service binding that records
     a core-native `session.EventCompact` checkpoint and the new engine rebuilds
     provider-visible context from the latest checkpoint forward.
   - Migrated baseline: `/new` and `/resume` now use the app-service TUI
     binding for core-native session start/list/load/replay, and resume lists
     can derive a display prompt from canonical user events when a session has
     no generated title yet.
   - Migrated baseline: `/agent list` and dynamic `/<agent> <prompt>` now have
     a service-native path for configured external ACP agents. The TUI binding
     records participant attachment, the user prompt to the participant, and
     the participant response as canonical core session events, then projects
     participant scope/origin back into the existing TUI event stream.
   - Migrated baseline: `/agent add custom <name> -- <command> [args...]` and
     `/agent remove <custom-agent>` now route through
     `internal/app/services.AgentService` and persist settings-backed external
     ACP agent descriptors in the shared app settings document.
   - Migrated baseline: `/agent add <builtin>` now reads a service-native
     built-in ACP catalog and persists the selected descriptor through
     `AgentService.RegisterBuiltin`, so non-install built-in registration no
     longer requires the old gatewayapp agent registry.
   - Migrated baseline: `/agent use <agent|local>` now records canonical
     controller handoff events through the app-service TUI gateway. When an ACP
     controller is active, normal TUI submissions are routed to the registered
     external ACP agent and recorded as controller-scoped canonical events. The
     app-service TUI gateway now derives the active controller from canonical
     events after each load, including the latest controller remote ACP session
     id, so follow-up prompts can reuse that id without storing controller state
     in TUI-only memory.
   - Migrated baseline: `/doctor` without repair now reads the same app-service
     status view as `/status`, including configured store URI, so the diagnostic
     display no longer needs the old gatewayapp doctor path for basic readiness
     checks.
   - Migrated baseline: `/connect` model completion and default
     context/output/reasoning values now come from
     `internal/app/services.Models()` through `BindAppServices`, including
     configured provider models and shared provider capability presets.
   - `surfaces/tui/app`, `surfaces/tui/gatewaydriver`, command registry,
     completion, connect wizard, status bar, renderer, transcript reducer,
     tool panels, approval UI, theme system, and attachment handling are not
     ported to `internal/app/services`.
   - Slash commands such as the `/connect` wizard shell, built-in
     `/agent install` and adapter update, plugin/static agent removal, remote
     ACP controller config commands, and `/doctor fix` still have old
     driver/app assumptions or missing service-native feature parity, so the
     old TUI stack cannot be removed yet.

3. Future APP surface
   - Migrated baseline: `internal/app/viewmodel.StatusView` and
     `internal/app/services.Status().View()` provide a service-native,
     surface-neutral status contract for runtime identity, current session
     summary, model selection, session mode, agents, resource counts, and store
     identity. This gives TUI and the future APP a shared status/diagnostics
     panel input without importing `gatewayapp` or any TUI package.
   - Still pending: task panels, settings mutation flows, richer model/provider
     selection views, agent management actions, approvals, transcript actions,
     and live event subscriptions still need APP-ready service/view-model
     contracts.

4. Headless CLI and ACP serving
   - Migrated baseline: a new service-native `internal/surface/headless`
     one-shot runner exists with text/JSON output and approval policy hooks.
   - Migrated baseline: production single-shot CLI execution and `caelis acp`
     stdio serving now enter the new local service stack instead of old
     `surfaces/headless` or `surfaces/acpserver`.
   - Still pending: old package cleanup after remaining entrypoints move,
     production settings/config parity, and richer ACP surface behavior.
   - The new ACP server now exposes session list/load/resume over the
     core-native session store and canonical ACP projector.
   - The new ACP server now exposes session model metadata, `session/set_model`,
     and model/reasoning `session/set_config_option` through
     `internal/app/services.Models()` rather than owning config semantics in
     the ACP surface. ACP prompts now enter through the shared app turn service
     when services are available, so selected model and reasoning overrides are
     part of the actual runtime model request instead of display-only state.
   - The new ACP server now handles `session/close` by cancelling active core
     runtime turns and treating already-idle sessions as successfully closed.
   - The new ACP server now exposes `session/set_mode`, session mode metadata,
     and the non-model `mode` config option through `internal/app/services.Modes()`.
   - Still pending: terminal integration, client mode flows, additional
     non-model config providers, and the full behavior covered by current
     public ACP e2e tests.

5. Settings, config, and model catalog
   - Migrated baseline: new app settings store, token redaction by default,
     provider profile/model config normalization, generated aliases/ids, model
     connect/delete/default/use service methods, session model override state,
     context window/output token fields, reasoning effort fields, auth/header
     fields, request-time model router, session reasoning override propagation,
     session mode service, and ACP stdio model/config/mode projection backed by
     shared app services. The TUI gateway driver also has a service-native
     binding for model connect/list/use/delete and session mode set/cycle.
   - Migrated baseline: shared model catalog data now provides configured
     provider models, built-in provider model presets, capability defaults, and
     reasoning levels to TUI/future APP setup flows through `ModelService`.
   - Migrated baseline: CodeFree OAuth login/model-selection ensure and refresh
     are now exposed through a replaceable `ModelService` auth contract, wired
     by the local app stack to the core-native CodeFree adapter and consumed by
     the TUI `/connect` binding.
   - Migrated baseline: standalone CLI doctor and host sandbox lifecycle
     subcommands now use the new local stack and shared app services instead
     of constructing `app/gatewayapp`.
   - Still pending: production CLI flag mapping, default home-dir bootstrapping,
     connect wizard persistence, remaining TUI command integration, remote
     provider model discovery UI data, additional non-model ACP config
     providers, non-host sandbox setup/repair config, and removal of the old
     `app/gatewayapp` config/model services once entrypoints move to the new
     stack.

6. Model providers
   - Migrated baseline: OpenAI-compatible Chat Completions, Anthropic,
     Anthropic-compatible, MiniMax, Gemini, CodeFree, DeepSeek, OpenRouter,
     Mimo/Xiaomi, Volcengine, Volcengine Coding Plan, and native Ollama
     `/api/chat` now implement `core/model.Provider` and can be selected by
     the new local stack and headless CLI.
   - Anthropic/MiniMax now have a core-native Messages API adapter with default
     endpoints, token lookup, API-version headers, text/image content mapping,
     tool-use/tool-result mapping, thinking signature replay metadata, usage
     mapping, and model listing.
   - Gemini now has a core-native API adapter with default endpoint, API-key
     header auth, text/image/file content mapping, tool-call/tool-result
     mapping, thought-signature replay metadata, JSON/schema output,
     reasoning config mapping, usage mapping, model listing, and settings
     endpoint normalization.
   - CodeFree now has a core-native chat adapter with default endpoint, clean
     Caelis credential loading, CodeFree request headers, OpenAI-compatible
     message/tool mapping, JSON output mode, usage mapping, model listing,
     OAuth login/model-selection ensure, credential refresh, and production
     headless CLI routing.
   - DeepSeek now has a core-native provider profile with default endpoint,
     token lookup, structured JSON output, reasoning content parsing, and
     thinking-mode request defaults for current reasoning models.
   - OpenRouter now has a core-native provider profile with default endpoint,
     token lookup, structured JSON-schema output, reasoning parsing, and Caelis
     attribution headers.
   - Mimo/Xiaomi and Volcengine now have core-native provider profiles with
     default endpoints, token lookup, structured JSON output, reasoning-content
     parsing, thinking payload mapping, and settings endpoint normalization.
   - Still pending: broader model discovery, detailed error mapping, SSE
     streaming, provider-specific
     tool/argument behavior beyond the migrated profiles, and removal of the
     corresponding old `impl/model/providers` code once no old-stack entrypoint
     requires it.

7. Sandbox backends and policy
   - The new stack only has a host sandbox adapter.
   - Migrated baseline: shared app sandbox status now projects the composed
     core sandbox runtime, and standalone CLI host
     `sandbox setup|fix|reset|clean` commands use that service as explicit
     no-op lifecycle operations.
   - macOS seatbelt, Linux bubblewrap/Landlock, Windows sandbox/helper/ACL
     repair, non-host sandbox setup/fix/reset/clean, network policy,
     writable/readable root policy, skill sandbox roots, rich route
     diagnostics, and production doctor repair reporting remain old-stack
     capabilities.

8. Built-in tools
   - Migrated baseline: `run_command`, `task`, filesystem tools `read_file`,
     `list_directory`, `glob_files`, `search_files`, `write_file`,
     `patch_file`, and `update_plan` now implement `core/tool.Tool` directly
     and are registered as builtin local stack tools through the new app
     registry.
   - `write_file` and `patch_file` are intentionally small exact-text tools
     built on the `core/sandbox.FileSystem` contract, so future sandbox
     backends can replace host execution without changing tool semantics.
   - `run_command` can now yield an async sandbox session through the
     `core/sandbox.Runtime.Start/Open` contract, and `task` can wait, write
     stdin, or cancel that yielded session without importing old task/runtime
     code.
   - Plan updates are no longer only display metadata in the new runtime:
     `update_plan` results are converted into canonical `session.EventPlan`
     records for ACP/TUI/APP projection.
   - Still pending: rich diff rendering metadata, spawn tool, durable task
     storage, task listing/tails, subagent task association, and display
     metadata for compact/rich tool panels still need core-native adapters.

9. Approval and permission policy
   - The new approval path supports allow/deny/ask, ACP permission response
     bridging, and a default local-stack ask policy for mutating filesystem
     tools. It now also supports a core-native per-session `manual` approval
     preset that forces approval prompts for every tool call while `auto-review`
     continues to use the configured policy.
   - Model-backed auto-review, richer policy presets, sandbox-aware permission
     escalation, allow-always/reject-always persistence, approval review
     transcript context, and richer denial metadata are not migrated.

10. Agents, subagents, and controller handoff
    - Migrated baseline: the new external ACP path covers participant
      invocation through shared app services, plugin-declared agent descriptors,
      local-stack invokers, and the app-service TUI dynamic `/<agent> <prompt>`
      path. Participant attachment, user prompts to participants, and external
      ACP responses are now canonical session events with participant
      scope/origin instead of TUI-only side effects.
    - Migrated baseline: custom external ACP agents now have app-service
      settings mutation, startup loading, dynamic invocation, and TUI
      add/remove/list coverage for settings-backed descriptors.
    - Migrated baseline: built-in ACP agent descriptors now live in a
      service-native app catalog and `/agent add <builtin>` registers them into
      the same settings-backed external ACP descriptor contract as custom
      agents.
    - Migrated baseline: ACP main-controller handoff now has an app-service
      path for registered external ACP agents. Handoffs are durable
      `EventHandoff` records, control-plane state is rebuilt from canonical
      events, and subsequent prompts can execute through the external ACP agent
      as controller-scoped session events. Controller-scoped response events
      now also feed the derived controller binding, so the latest remote ACP
      session id is carried forward into the next controller prompt.
    - Still pending: built-in ACP adapter install/update, self-agent spawning,
      durable sidecar continuation across restarts, delegated subagent tasks,
      durable remote controller process/session lifecycle beyond canonical
      remote session id reuse, plugin/static agent removal, remote ACP
      controller model/mode config commands, and terminal previews remain
      old-stack.

11. Task runtime and async work
    - Migrated baseline: host async command sessions now implement the
      `core/sandbox.Session` contract, and the core-native `task` tool can
      wait, write stdin, and cancel yielded shell work.
    - Still pending: durable task storage, task listing, output tail cursors
      across process restarts, SPAWN/subagent task association, terminal
      previews, and production surface controls still live in old paths.

12. Compaction and replay validation
    - Migrated baseline: manual TUI compaction through `internal/app/services`
      now records a canonical compact checkpoint event, and runtime model
      context reconstruction starts from the latest compact checkpoint.
    - Still pending: model-backed compact summary generation, automatic
      compaction, usage accounting after compaction, compaction prompt policy,
      and full runtime-vs-reload semantic round-trip tests remain to be
      migrated.

13. Prompt, skills, and resources
    - The new discovery path reads plugin prompts, `AGENTS.md`, and skill
      metadata.
    - Built-in system prompt parity, session `-system-prompt`, skill content
      expansion, skill sandbox roots, prompt policy controls, and prompt budget
      handling still need migration.

14. Rendering and display semantics
    - The new view model is intentionally small.
    - Rich transcript rendering, reasoning/narrative smoothing, ACP tool
      content formatting, diff panels, mutation panels, exploration compaction,
      status/usage display, and UI-only live chunk handling remain surface-local
      old-stack work.

15. Session listing and resume workflows
    - Migrated baseline: `core/session.Store` and `core/runtime.Engine` now
      expose session listing as a durable contract, and memory, JSONL, and
      SQLite stores implement app/user/workspace/CWD/search filters, offset
      pagination, event counts, and last-event timestamps. `internal/app/services`
      exposes the same list path with runtime workspace defaults so TUI and
      future APP surfaces can share the same resume data source.
    - Migrated baseline: the new ACP stdio server now serves `session/list`,
      `session/load`, and `session/resume` from the same core-native session
      contract, and `session/load` replays canonical events as ACP
      `session/update` notifications.
    - Migrated baseline: TUI `/new` and `/resume` now use the app-service
      driver binding over the same core session list/load/replay contract, with
      canonical user-event prompt fallback for untitled sessions.
    - Still pending: richer title generation, metadata search, cross-workspace
      filters, current on-disk legacy session layout migration, and reload UX
      polish are not implemented.

### Next Migration Milestones

Recommended sequence:

1. Finish wiring settings/model services into product entrypoints and TUI
   commands.
2. Port provider catalog and at least the current configured providers behind
   `core/model.Provider`.
3. Port sandbox router/backends and permission policy before moving mutating
   tools.
4. Port spawn and durable task runtime behavior behind `core/tool.Registry`
   and `internal/engine/tasks`.
5. Port TUI driver commands, including the remaining built-in agent management
   actions, to `internal/app/services`, preserving existing rendering as
   surface-local code.
6. Expand shared APP view models for settings, agent management, richer model
   selection, approvals, tasks, and live transcript actions.
7. Migrate compaction, task runtime, subagent lifecycle, and controller handoff
   to canonical events.
8. Add full store round-trip and ACP projection parity tests for product flows.
9. Delete the old runtime stack once the new entrypoints satisfy current CLI,
    TUI, ACP, and eval behavior.

## Validation Gates

Every phase should keep these gates:

- `go test` for affected packages.
- Architecture lint for dependency direction.
- Store round-trip tests comparing rebuilt model context with runtime-produced
  context.
- ACP replay projection tests.
- TUI and future APP contract tests against the same app service fixtures.
- `git diff --check`.

## Decision Summary

The long-term target is:

> Caelis as a small ACP-native runtime kernel with stable core contracts,
> deterministic canonical events, and a plugin/adapter ecosystem that can swap
> model providers, stores, sandbox backends, tools, external ACP agents, and
> surfaces without rewriting the orchestration layer.

This keeps the spirit of Pi Agent's small core and flexible extensions, while
making the necessary Caelis-specific choice: ACP is not just an extension. ACP is
the native protocol boundary around which the runtime and ecosystem are built.
