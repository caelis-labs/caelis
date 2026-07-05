# Caelis Architecture

Target direction:

```text
Presentation surfaces -> Control layer -> Agent Runtime / SDK
```

The current tree is part way through SDK extraction. `agent-sdk/*` is the
reusable module boundary. Remaining `ports/*` packages are product-host/control
contracts, and remaining `internal/*` packages are Caelis application glue.

## Layers

- **Presentation surfaces**: TUI, ACP stdio/server, headless CLI, and future
  GUI. Surfaces consume ACP-style `eventstream.Envelope` payloads plus
  documented `_meta` extensions. They render and collect input; they must not
  own model, tool, sandbox, policy, persistence, or runtime semantics.
- **Control layer**: application orchestration. It assembles runnable agents,
  owns lifecycle, permissions, policy/review routing, Guardian/Reviewer/system
  agents, future Agent Manage Loop coordination, and external ACP-agent bridges.
- **Agent Runtime / SDK**: reusable agent-building modules such as model, tools,
  skills, sandbox, stream, task, subagent, provider adapters, and turn mechanics.
  Runtime modules should not depend on presentation or one controller.

ACP is the surface-facing protocol, not necessarily the only internal event
model. The control layer may use runtime/control events internally and project
them to ACP-style surface events. Projection details that need more space live in
[docs/acp-projection-architecture.md](acp-projection-architecture.md).

## Current Map

- `cmd/caelis`, `internal/cli`: process entry and mode selection.
- `surfaces/*`: presentation adapters.
- `protocol/acp`: ACP schema, eventstream envelopes, projection helpers, and
  documented `_meta` contracts.
- `agent-sdk/*`: reusable SDK module. It owns runtime, model, tool, session,
  sandbox, task, policy, skill, and display contracts and reusable
  implementations.
- `ports/controlcommand`, `ports/controlprompt`: transitional command catalog
  plus prompt request/result parsing contracts.
- `internal/controlpromptrouter`: shared app-control slash orchestration over
  `protocol/acp/control.Service`.
- `app/gatewayapp`, `internal/kernel`, `protocol/acp/control`: current control
  layer hotspots.
- `ports/gateway`, `ports/plugin`, `ports/controlcommand`,
  `ports/controlprompt`, and `ports/agentprofile`: product-host contracts that
  stay outside the SDK.
- `internal/acpagentbridge`: Caelis control-layer bridge for ACP agent-side
  runtime, controller, subagent, and terminal integration.
- `platform/*`: product support code for platform-specific host behavior.

## SDK Boundary

The Agent SDK is a nested Go module at
`github.com/caelis-labs/caelis/agent-sdk`. It should remain usable below Caelis
as a standalone dependency. SDK packages must not depend on:

- `app/*`
- `surfaces/*`
- `protocol/acp/*`
- product-host `ports/*` packages
- repository `internal/*` packages outside the SDK module

Product hosts provide model, session, sandbox, tool, policy, and task
implementations through SDK contracts instead of making the runtime know where
credentials, state, or execution environments live.

Current SDK package ownership:

- `agent-sdk`: cross-domain public contracts for agent specs, turn requests,
  runtime events, capabilities, approvals, handoff, usage, and stable errors.
- `agent-sdk/approval`: approval review contracts.
- `agent-sdk/display`: display helpers for runtime and tools.
- `agent-sdk/model`: model contracts, provider implementations, and catalog
  data.
- `agent-sdk/policy`: policy presets and permission helpers.
- `agent-sdk/runtime`: local agent runtime, controller contracts, and
  control-plane helpers.
- `agent-sdk/sandbox`: sandbox contracts and local implementations.
- `agent-sdk/session`: session contracts and stores.
- `agent-sdk/skill`: skill discovery and builtin skill tooling.
- `agent-sdk/task`: task and subagent contracts.
- `agent-sdk/tool`: tool registry contracts and builtin tools.

The current migration has moved reusable runtime, model, tool, session,
sandbox, task, policy, skill, and display contracts and implementations into
`agent-sdk/*`. SDK-owned `ports/*` and global `impl/*` compatibility paths have
been removed; the remaining `ports/*` packages are product-host contracts, and
Caelis ACP agent bridge code now lives under `internal/acpagentbridge`.

Repeatable SDK boundary gates:

- `make sdk-standalone-check`: copy `agent-sdk/` outside the product tree and
  prove it resolves without the root module.
- `make sdk-external-replace-check`: copy the product host without in-tree
  `agent-sdk/` and prove it builds against an external SDK copy.
- `make sdk-external-consumer-check`: compile a minimal external consumer
  against public SDK packages.
- `make commit-check`: run formatting, lint, arch lint, vet, tests, SDK boundary
  gates, and builds.

The next architecture step is to harden the standalone SDK module and only then
consider a physical repository split.

## Durable State

`agent-sdk/session.Event` is the source of truth for persisted runtime context.
Durable model-visible facts require canonical payloads:

- `Event.Message` for model messages;
- `Event.Tool` for tool calls and results;
- `PlanPayload` for plan state;
- `EventProtocol{Method, Update, Permission}` for control-plane projection.

`Event.Protocol.Update` and `_meta` are not substitutes for canonical model
state. `_meta` is display/debug or documented replay metadata.

Visibility categories:

- `canonical`: persisted, replayed, and model-visible when it carries model
  semantics.
- `mirror`: persisted/replayed as a client-facing mirror, not a second model
  context.
- `ui_only`, `overlay`, `notice`: not durable parent model context.

Subagent stream chunks are `ui_only`; the parent receives subagent output
through durable `SPAWN`/`TASK` tool results.

## Migration Rules

- Prefer bounded, high-ROI boundary improvements over broad rewrites.
- Do not add abstractions only for future possibilities.
- When compatibility fallbacks are necessary, document owner, scope, and removal
  condition.
- Keep surfaces on the shared ACP-style protocol; avoid surface-private replay
  or terminal paths.
- Persistence/replay changes need round-trip tests comparing rebuilt model
  context with runtime-produced context.
