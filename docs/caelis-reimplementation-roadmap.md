# Caelis Reimplementation Roadmap

Status: architecture reference and finish-line checklist
Last updated: 2026-06-01

## Product Shape

Caelis is an ACP-native agent runtime and gateway. Its core should stay small:
canonical session state, runtime orchestration, approval/policy coordination,
ACP ingress/egress, and replaceable contracts. Everything else should be a
plugin, adapter, registry contribution, app service, or surface projection.

The product has three equal surface families:

- TUI: terminal-first interactive product surface.
- Future APP: peer desktop/app surface over the same app services and
  view-models.
- ACP/CLI: headless and protocol-native access for clients such as Zed and
  external ACP agents.

Surfaces may render differently, but they must share the same canonical events,
app-service payloads, settings contracts, command semantics, and extension
ecosystem.

## Architecture Principles

- ACP is a native product boundary, not an afterthought adapter.
- `core/session.Event` is the durable source of truth for model-visible state,
  tool execution state, lifecycle state, approvals, provider metadata, replay
  signatures, and compaction context.
- ACP `session/update` and Caelis surface payloads are projections from
  canonical events or shared app-service view-models.
- External ACP input is normalized into canonical session events before it is
  stored or replayed.
- Prompt materialization belongs to the core/service path. Policy may include
  or exclude sources, but surfaces should not assemble runtime prompts.
- Model providers, sandbox backends, stores, tools, prompt sources, skills, and
  ACP agents are replaceable through explicit contracts and registries.
- Before `v1.0.0`, prefer clean schemas and boundaries over legacy
  compatibility branches.

## Package Layers

```text
cmd/caelis/
  main.go                    # binary entrypoint only

core/
  config/                    # typed configuration contracts
  model/                     # provider request/response contracts
  plugin/                    # contribution manifests and descriptors
  runtime/                   # engine/session/turn contracts
  sandbox/                   # sandbox runtime contracts
  session/                   # canonical events, snapshots, stores, indexes
  tool/                      # tool definitions, calls, results, metadata

internal/engine/
  gateway/                   # ACP-native session and turn coordination
  loop/                      # model/tool execution loop
  context/                   # model-context reconstruction
  approval/                  # approval and escalation coordination
  compaction/                # compaction state and replay boundaries
  tasks/                     # async task lifecycle coordination
  control/                   # controller and participant orchestration

internal/app/
  local/                     # default composition root
  services/                  # shared product API for all surfaces
  viewmodel/                 # surface-neutral payloads and panels
  settings/                  # persisted app settings and policy
  prompt/                    # prompt assembly from governed sources
  resources/                 # workspace/global resource discovery
  registry/                  # provider/store/sandbox/tool/agent registries

internal/adapters/
  model/                     # concrete model providers
  store/                     # jsonl/sqlite/memory store adapters
  sandbox/                   # host/seatbelt/bwrap/landlock/windows adapters
  tool/                      # built-in and contributed tools
  acpagent/                  # external ACP agent adapters

protocol/acp/
  schema/                    # ACP and Caelis projection schemas
  jsonrpc/                   # JSON-RPC framing
  transport/                 # stdio/process transports
  client/                    # ACP client
  server/                    # ACP server
  projector/                 # canonical event to ACP projection

internal/surface/
  acpserver/                 # `caelis acp` surface
  headless/                  # one-shot CLI surface

surfaces/tui/
  app/                       # TUI presentation state
  gatewaydriver/             # TUI driver over app services
  driver/                    # terminal driver contracts
```

## Boundary Rules

- `core/*` defines durable contracts and must not import `internal/*` or
  surfaces.
- `internal/engine/*` owns runtime orchestration and must not depend on TUI or
  APP presentation code.
- `internal/app/services` is the product facade consumed by TUI, future APP,
  headless CLI, and ACP server surfaces.
- `internal/app/viewmodel` contains shared payloads, not UI widgets.
- `internal/adapters/*` implements contracts and must not own product flow.
- `protocol/acp/*` owns protocol schema, transport, server/client code, and
  deterministic projections.
- `surfaces/*` render and collect user intent only; they must not own model,
  sandbox, store, replay, or prompt semantics.

## Current Baseline

- Old broad `ports`, legacy `kernel` facade, and `app/gatewayapp` style
  composition are no longer part of the target shape.
- Shared command completions, controller/task/settings panels, and core service
  payloads are exposed through app services.
- ACP command surface updates are projected from shared app-service view-models.
- JSONL, SQLite, and memory stores implement replaceable canonical event stores;
  optional indexed event access is available behind the shared session contract.
- Prompt policy controls cover agent instructions, plugin prompts, environment
  context, and skill loading while keeping prompt assembly service-native.

## Todo

- Build the future APP as a peer surface over `internal/app/services` and
  `internal/app/viewmodel`; keep APP controllers, rendering, and navigation
  surface-local.
- Finish live Windows host smoke/e2e validation for async sessions and sandbox
  lifecycle behavior without restoring removed router, preset, or tool stacks.
- Continue store round-trip and ACP projection parity tests whenever new
  lifecycle surfaces, task states, or app-service payloads are added.
- Expand indexed history usage where it materially improves resume, task lists,
  or transcript navigation, while keeping JSONL and SQLite replaceable adapters.
- Keep pruning empty packages and surface-local leftovers after each migration
  checkpoint.
