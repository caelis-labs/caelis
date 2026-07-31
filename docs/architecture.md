# Caelis Architecture

Target direction:

```text
Presentation surfaces -> Control layer -> Agent Runtime / SDK
```

`agent-sdk/*` is the long-lived reusable package and dependency boundary inside
the root `github.com/caelis-labs/caelis` Go module. It has no separate module,
version, release, or test lifecycle. Coherent, stable product-control
capabilities belong in `control/*`. Today `app/gatewayapp`, `internal/kernel`,
other `internal/control*` packages, and `control/*` form one Control
implementation domain; physical package movement is not itself an architecture
goal. The transitional `ports/*` tree has retired through bounded,
independently verified slices and must not be recreated.

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
`protocol/acp` implementation. The root `protocol/acp` packages own ACP wire
schema, transport compatibility, and surface projection. Canonical runtime events still
carry model and tool truth below that protocol view. More detail lives in
[docs/agent-sdk-boundary.md](agent-sdk-boundary.md) and
[docs/acp-projection-architecture.md](acp-projection-architecture.md).

Document responsibilities are intentionally separate:

- this file owns the layer and repository map;
- [Agent SDK Boundary](agent-sdk-boundary.md) owns normative SDK/Control/ACP
  decisions;
- [ACP Projection Contract](acp-projection-architecture.md) owns semantic,
  wire, and surface projection boundaries;
- [Control Convergence](control-convergence.md) owns the desired Control
  direction and end-state constraints;
- [Control Host, Bar, and Pet](control-host-bar-pet-design.md) owns the target
  multi-client host topology and desktop activity presentation contract;
- [Release](release.md) owns release mechanics.

## Current Map

- `cmd/caelis`, `internal/cli`: process entry and mode selection.
- `surfaces/*`: presentation adapters.
- `protocol/acp`: product ACP wire schema and transport, eventstream envelopes,
  projection helpers, compatibility handling, and documented `_meta`
  contracts. Reusable normalized ACP semantics may live in the SDK.
- `agent-sdk/*`: reusable SDK package tree. It owns runtime, model, tool, session,
  sandbox, task, policy, skill, and display contracts and reusable
  implementations.
- `control/modelcatalog`: Control-owned provider/model directory, concrete model
  capability metadata, provider overlays, and the embedded models.dev snapshot.
  Known providers use maintained metadata; generic compatible endpoints do not
  inherit another vendor's model list. Models without explicit Control metadata
  require custom advanced configuration.
- `control/modelconfig`: Control-owned provider and endpoint onboarding
  templates, provider authentication orchestration, authenticated model
  selection, provider endpoint persistence, and complete SDK model
  construction. Provider endpoints are infrastructure and are not selectable
  `ModelProfile` values. Persisted AppConfig contains only an opaque credential
  reference; credential material remains in the Control credential store.
- `control/modelprofile`: the single product-level selectable model catalog.
  Every entry references either one configured provider model or one external
  ACP Agent plus one exact remote model, defaults, and canonical-to-wire effort
  mapping. Its global default is exactly one `ModelProfile` ID plus one
  canonical effort; provider model IDs and display aliases are derived and are
  not parallel persisted defaults. `/connect` produces these profiles for both
  backend kinds.
- `control/agentbinding`: fixed handle bindings. Breeze, Orbit, Zenith,
  Guardian, and Reviewer bind to exactly one `ModelProfile` and one explicit
  canonical effort.
- `control/placement`: the Control-owned placement boundary. Fixed-handle work
  uses its handle resolver; participant attach uses its explicit profile-and-
  effort selector. Both paths consume the same immutable snapshot and sealing
  rules before durable work is prepared.
- `control/taskstream`: the Control-owned, Session-authorized directory and
  transient record service for existing Tasks. A stream is addressed only by
  `(SessionID, TaskID)`, while the directory exposes the Session-unique public
  Task handle used by people, models, and Task control. Surfaces resolve that
  handle or its typed parent-tool relation through the directory before using
  the opaque TaskID; they never recover identity from `_meta`. This adds no
  Execution lifecycle, durable output store, or schema.
  `protocol/acp/taskstream` is the protocol adapter that projects those records
  into transient Envelopes for Surfaces.
- `control/agents`: external ACP connection identity and lifecycle. One stable
  Agent represents one connection; sibling remote models are separate
  `ModelProfile` entries and never become synthetic Agents or Agent-owned
  defaults. Live ACP Session IDs remain execution state and are never persisted
  as discovery configuration. Authentication recovery, Agent-default behavior,
  and Registry snapshot maintenance are defined in
  [External ACP Agents](external-acp-agents.md).
- `control/plugin`: Control-owned plugin configuration, manifest discovery,
  identity, marketplace/install resolution, lifecycle mutation, and normalized
  hook, skill, MCP server, and external Agent contributions. Its `Info` view is
  also Control-owned and may include live MCP server status. The application
  host supplies the product data root used for managed installs, atomic state
  persistence, active-Turn fencing, Runtime replacement and rollback, plus the
  read-only live MCP status probe required to build that view.
- `control/client`: the complete product-facing Control client. It owns trusted
  principals, transport-neutral commands and outcomes, Session authorization,
  list/bootstrap/reconnect state, feed/replay coordination, legacy-child-mirror
  filtering, approval recovery, the aggregate client, the durable idempotency
  operation ledger, and the Session lifecycle write gate. Its feed uses the
  shared ACP projection and `eventstream.Envelope` vocabulary without moving
  Control authorization, state, or broker ownership into `protocol/acp`.
- `internal/controlclient/turningress`: private main-Turn ingress glue between
  `internal/kernel` handles and the Control-owned Session feed. Task output
  never fans into this ingress and cannot delay its terminal.
- `control/status`: Control-owned product status read model shared by client
  and prompt surfaces without presentation formatting. Its nested sandbox
  `Setup` and named checks are the sole setup-state source; adapters normalize
  any transitional host fields before constructing this read model. Structured
  `SessionUsageTotal` and `SessionUsageByModel` values are likewise the sole
  cumulative Session-usage sources; the public model carries no flat mirrors.
- `control/client/wirev1`: the versioned, Control-client-bound HTTP/SSE schema
  bindings, generated types, and strict JSON/Envelope codec. Because the codec
  serializes `control/client` domain values directly, it stays with that
  semantic owner instead of pretending to be a product-neutral protocol.
- `control/client/httpclient`: the authenticated remote implementation of the
  principal-bound `control/client.SessionClient`. In-process clients use
  `control/client.BindSessionClient`; both expose the same typed contract.
- `app/controlserver`: the Control Host's HTTP handler and production listener,
  including authentication, request policy, TLS, token-file policy, and
  shutdown drain. It maps only the bounded Session-client protocol and depends
  on explicit Control contracts rather than `gatewayapp.Stack`.
- `internal/controlprompt`: current Control-owned surface-neutral prompt input
  contract, private prompt facade, command catalog, parsing helpers,
  connect-wizard state, and shared slash orchestration. It is not a product
  client API and must not accumulate transport or presentation semantics.
- `surfaces/promptview`: shared text/display projection of structured prompt
  results and Control status snapshots.
- `internal/controlassembly`: product Agent assembly resolution.
- `internal/controlplane`: shared-ledger routing, endpoint lifecycle/recovery,
  and handoff coordination.
- `app/gatewayapp`: the current product Control host and composition entry. Its
  app-scoped Session Runtime registry routes public Control execution by durable
  Session ID. A stateless workspace assembler reads current app configuration
  and workspace files on the first execution after a Session has no live
  activation, then the detached Session Runtime keeps that composition fixed
  until release. Durable Session creation, inspect, and reconnect allocate no
  execution Runtime. Configuration changes therefore affect later activations
  without mutating active prompt, skill, sandbox, model, or placement prefixes.
  No workspace configuration generation or Session-to-generation binding is
  persisted; durable Sessions store only canonical workspace identity.
  Assembly and app configuration mutation share one Host lock, while release
  first hides its Runtime from routing, waits already-routed synchronous
  mutations, and shutdown drains in-flight assembly and release before closing
  all Session and transitional default Gateways. Until TUI/headless ingress
  uses the typed Session client, direct default-Stack Sessions retain an
  explicit process-local ownership marker; the marker is a migration fence, not
  durable configuration, and is removed with the private default-Gateway path.
  `UserID` remains a compatibility authorization/persistence field and is not a
  Runtime partition key.
- `internal/kernel`: Control-owned Session/Turn coordination, gateway
  contracts, and their current implementation. The contracts formerly exposed
  by `ports/gateway` now have one authority here rather than a forwarding port.
- other `internal/control*` packages: current Control integration
  implementations that may converge with adjacent `app/*` and `control/*`
  ownership before any later package split.
- `app/gatewayapp/controladapter`: transitional in-process implementation of
  the private `internal/controlprompt.Service` prompt facade. Do not add
  product-client operations to this aggregate interface or recreate `ports/*`;
  new capabilities belong in coherent `control/*` packages.
- `internal/acpagentbridge`: external ACP transport, process-lifecycle, and
  product integration adapters that make external endpoints implement the same
  SDK controller/participant contracts used by built-in Agents. Product
  assembly supplies any plain-text prompt-result projector; the bridge does not
  import presentation packages.
- `platform/*`: product support code for platform-specific host behavior.

## SDK Boundary

The Agent SDK is an ordinary package tree in the root Go module, imported under
`github.com/caelis-labs/caelis/agent-sdk/...`. It is versioned and released with
the Caelis root module. The package tree remains reusable below the application;
module extraction, physical repository extraction, and additional adapter
modules are not current goals. SDK packages must not depend on:

- `control/*`
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

Package-level ownership and the supported public import set are defined by
[Agent SDK Boundary](agent-sdk-boundary.md),
[`agent-sdk/supported-packages.txt`](../agent-sdk/supported-packages.txt), and
the enforced architecture gates.

Control fences each canonical Session Turn across its complete asynchronous
producer lifetime. Agent-loop safety remains inside the Agent implementation;
one Agent must not inspect or cancel an unrelated external endpoint.

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
