# Caelis Architecture

Target direction:

```text
Presentation surfaces -> Control layer -> Agent Runtime / SDK
```

Current packages are transitional. Do not treat today's
`cmd/caelis -> internal/cli -> app/gatewayapp -> internal/kernel ->
ports/impl/protocol/surfaces` path as the desired end state.

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
them to ACP-style surface events.

## Current Map

- `cmd/caelis`, `internal/cli`: process entry and mode selection.
- `surfaces/*`: presentation adapters.
- `protocol/acp`: ACP schema, eventstream envelopes, projection helpers, and
  documented `_meta` contracts.
- `ports/controlcommand`, `ports/controlprompt`: transitional command catalog
  plus prompt request/result parsing contracts.
- `internal/controlpromptrouter`: shared app-control slash orchestration over
  `protocol/acp/control.Service`.
- `app/gatewayapp`, `internal/kernel`, `protocol/acp/control`: current control
  layer hotspots.
- `ports/*`: public contracts. Keep runtime contracts separate from app-control
  contracts as boundaries become clearer.
- `impl/*`: concrete runtime implementations. Avoid surface/controller imports
  unless a package is explicitly an adapter.

## Durable State

`ports/session.Event` is the source of truth for persisted runtime context.
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
