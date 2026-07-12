# Caelis Architecture

Target direction:

```text
Presentation surfaces -> Control layer -> Agent Runtime / SDK
```

`agent-sdk/*` is the long-lived reusable package and dependency boundary inside
the root `github.com/caelis-labs/caelis` Go module. It has no separate module,
version, release, or test lifecycle. Remaining `ports/*` packages are
product-host/control contracts, and remaining `internal/*` packages are Caelis
application glue.

## Layers

- **Presentation surfaces**: TUI, ACP stdio/server, headless CLI, and future
  GUI. Surfaces consume ACP-style `eventstream.Envelope` payloads plus
  documented `_meta` extensions. They render and collect input; they must not
  own model, tool, sandbox, policy, persistence, or runtime semantics.
- **Control layer**: application orchestration. It assembles runnable agents,
  owns lifecycle, permissions, policy/review routing, Guardian/Reviewer/system
  agents, future Agent Manage Loop coordination, and built-in/external ACP
  endpoint lifecycle. It alone selects the active controller and authorizes a
  handoff; an Agent may suggest a transition but cannot commit one.
- **Agent Runtime / SDK**: reusable agent-building packages such as model, tools,
  skills, sandbox, stream, task, subagent, provider adapters, and turn mechanics.
  It may own normalized ACP-compatible controller, participant, event,
  permission, cancellation, and transfer contracts. Runtime packages should not
  depend on presentation, product assembly, or one transport implementation.

ACP is Caelis's native Agent interoperability and control language as well as
the surface-facing protocol. Built-in and external Agents should expose the
same effective session, prompt, update, permission, cancellation, controller,
and participant semantics. Native ACP means semantic equivalence; an in-process
built-in Agent does not need to serialize calls through JSON-RPC.

The SDK owns reusable normalized semantics without importing the Caelis product
`protocol/acp` implementation. The root `protocol/acp` packages own wire schema,
transport compatibility, and surface projection. Canonical runtime events still
carry model and tool truth below that protocol view. More detail lives in
[docs/agent-sdk-boundary.md](agent-sdk-boundary.md) and
[docs/acp-projection-architecture.md](acp-projection-architecture.md).

Document responsibilities are intentionally separate:

- this file owns the layer and repository map;
- [Agent SDK Boundary](agent-sdk-boundary.md) owns normative SDK/Control/ACP
  decisions;
- [Agent SDK Usage and Compatibility](agent-sdk-usage.md) owns consumer-facing
  contracts and current limitations;
- [ACP Projection Architecture](acp-projection-architecture.md) owns semantic,
  wire, and surface projection boundaries;
- [Control and Client Protocol Roadmap](control-client-roadmap.md) owns the
  ordered next-stage implementation plan and milestone acceptance criteria;
- [Release](release.md) owns release and post-publish verification mechanics.

## Current Map

- `cmd/caelis`, `internal/cli`: process entry and mode selection.
- `surfaces/*`: presentation adapters.
- `protocol/acp`: product ACP wire schema and transport, eventstream envelopes,
  projection helpers, compatibility handling, and documented `_meta`
  contracts. Reusable normalized ACP semantics may live in the SDK.
- `agent-sdk/*`: reusable SDK package tree. It owns runtime, model, tool, session,
  sandbox, task, policy, skill, and display contracts and reusable
  implementations.
- `ports/controlcommand`, `ports/controlprompt`: transitional command catalog
  plus prompt request/result parsing contracts.
- `internal/controlpromptrouter`: shared app-control slash orchestration over
  `protocol/acp/control.Service`.
- `internal/controlassembly`: product Agent assembly and profile resolution.
- `internal/controlplane`: shared-ledger routing, endpoint lifecycle/recovery,
  and handoff coordination.
- `app/gatewayapp`, `internal/kernel`, `protocol/acp/control`: remaining control
  layer hotspots and host integration.
- `ports/gateway`, `ports/plugin`, `ports/controlcommand`,
  `ports/controlprompt`, and `ports/agentprofile`: product-host contracts that
  stay outside the SDK.
- `internal/acpagentbridge`: external ACP transport, process-lifecycle, and
  product integration adapters that make external endpoints implement the same
  SDK controller/participant contracts used by built-in Agents.
- `platform/*`: product support code for platform-specific host behavior.

## SDK Boundary

The Agent SDK is an ordinary package tree in the root Go module, imported under
`github.com/caelis-labs/caelis/agent-sdk/...`. It is versioned and released with
the Caelis root module. The package tree remains reusable below the application;
module extraction, physical repository extraction, and additional adapter
modules are not current goals. SDK packages must not depend on:

- `app/*`
- `surfaces/*`
- `protocol/acp/*`
- product-host `ports/*` packages
- repository `internal/*` packages outside the `agent-sdk` package tree

Product hosts provide model, session, sandbox, tool, policy, and task
implementations through SDK contracts instead of making the runtime know where
credentials, state, or execution environments live.

The ban on importing the root `protocol/acp/*` implementation does not ban ACP
semantics from the SDK. Dependency direction is from the product wire and
projection implementation toward reusable SDK contracts, never the reverse.

Current SDK package ownership:

- `agent-sdk`: cross-domain public contracts for agent specs, turn requests,
  runtime events, capabilities, approvals, neutral handoff/transfer values,
  usage, and stable errors. Handoff policy and target selection remain
  app-owned.
- `agent-sdk/approval`: approval review contracts.
- `agent-sdk/display`: display helpers for runtime and tools.
- `agent-sdk/model`: model contracts, provider implementations, and catalog
  data.
- `agent-sdk/policy`: policy presets and permission helpers.
- `agent-sdk/runtime`: local agent runtime, reusable ACP-compatible endpoint and
  controller contracts, turn mechanics, and low-level control-plane mechanisms.
  Product assembly, Agent selection, Manage Loop policy, and ownership-transfer
  coordination stay outside the SDK.
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

- `make arch-lint`: reject direct SDK dependencies on non-SDK Caelis packages.
- `make sdk-boundary-check`: reject nested module metadata, check production and
  test dependency closure, and compile public SDK imports from an external
  consumer of the root module.
- `make test`: test the root module, including all SDK packages, once.
- `make commit-check`: run formatting, lint, architecture and package-boundary
  checks, vet, tests, and builds.

The implementation centralizes update and coordination semantics, keeps
product assembly and handoff policy in Control, uses neutral task principals
and roles, and routes system Agents through the common Runtime pipeline.
Durable continuation is explicitly process-local live attach, while the
production Control host owns fenced cross-Runtime Session execution leases.
The lease serializes one canonical Turn rather than one Agent identity: local
and ACP controllers plus Side ACP or Reviewer participant prompts use the same
fenced envelope. Participant lifecycle is explicit Control metadata with
revision/delegation/generation CAS; handoff acquires the exclusive lease before
endpoint activation and binding commit. ACP event forwarding preserves the
owning Turn fence instead of becoming an unscoped writer.

Execution capability wiring and the liveness watchdog are Control-owned. The
watchdog is a generation-tail loop probe for repeated pure-text cycles and
repeated content-plus-tool-argument steps, including normalized protocol-only
ACP tool calls. It resets pure-text evidence at tool boundaries and may
Interrupt only after high-confidence evidence is reviewed. Review is
asynchronous and bounded to eight active pipelines; saturation drops evidence
rather than queueing or cancelling a Turn. Reviewer/checkpoint failures do not
delay or fail normal completion, and normal finish invalidates late decisions.
This does not restore a fixed SDK step or wall-clock budget. Product source
policy no longer lives in SDK task code. Module or repository extraction is not
a goal.

## Durable State

`agent-sdk/session.Event` is the source of truth for persisted runtime context.
Durable model-visible facts require canonical payloads:

- `Event.Message` for model messages;
- `Event.Tool` for tool calls and results;
- `PlanPayload` for plan state;
- `EventProtocol{Method, Update, Permission}` for ACP-compatible coordination
  facts and replayable control-plane projection.

ACP-native does not make raw protocol payloads the only durable truth.
`Event.Protocol.Update` and `_meta` are not substitutes for canonical model
state. `_meta` is display/debug or documented replay metadata.

Visibility categories:

- `canonical`: persisted, replayed, and model-visible when it carries model
  semantics.
- `mirror`: persisted/replayed as a client-facing mirror, not a second model
  context.
- `ui_only`, `overlay`, `notice`: not durable parent model context.

Subagent stream chunks are `ui_only`; the parent receives subagent output
through durable `Spawn`/`Task` tool results.

## Migration Rules

- Prefer bounded, high-ROI boundary improvements over broad rewrites.
- Do not add abstractions only for future possibilities.
- Do not add a deterministic workflow graph, node/edge DSL, or SDK-owned
  workflow executor. Dynamic orchestration belongs to the Control-layer Agent
  Manage Loop.
- Do not expose an LLM-facing handoff tool. Only explicit user control or
  Control-layer policy may transfer a controller epoch.
- When compatibility fallbacks are necessary, document owner, scope, and removal
  condition.
- Keep surfaces on the shared ACP-style protocol; avoid surface-private replay
  or terminal paths.
- Persistence/replay changes need round-trip tests comparing rebuilt model
  context with runtime-produced context.
