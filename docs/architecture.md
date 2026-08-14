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
- [Product Testing](testing.md) owns deterministic cross-layer regression,
  physical TUI observation, and quality-gate placement;
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
  API-key material is stable Host endpoint state rather than model-catalog
  state: deleting the last model retains it so pinned Runtime generations keep
  their immutable credential source and a later `/connect` can reuse it.
  Explicit credential forgetting is a separate future Host capability that
  must fence every cross-process Runtime generation; until then the credential
  lifetime and removal condition are the Host state directory itself.
  Interactive Codex and Grok authentication is admitted by a
  fail-fast, process-local gate per OAuth flow. It never creates a durable
  command-lock file or waits behind another user interaction. Short credential
  replacement transactions retain their platform file locks.
- `control/modelprofile`: the single product-level selectable model catalog.
  Every entry references either one configured provider model or one external
  ACP Agent plus one exact remote model, defaults, and canonical-to-wire effort
  mapping. Its global default is exactly one `ModelProfile` ID plus one
  canonical effort; provider model IDs and display aliases are derived and are
  not parallel persisted defaults. `/connect` produces these profiles for both
  backend kinds. A model selection made without a selected Session changes this
  persisted Host default for future Sessions. Once a Session is selected, model
  selection changes only that Session's revision-fenced override. Process
  startup flags may choose a different effective model for new work without
  mutating the persisted Host default; Host status must report that effective
  selection until an explicit Host model mutation supersedes it.
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
- `control/agents`: external ACP connection identity and lifecycle. Host-scoped
  prepare, explicit authentication, and connect operations use the shared
  Control command ledger; a secret-free, expiring preparation record binds
  discovery evidence to the principal and exact command receipt. One stable
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
  compare-and-save persistence, candidate validation before commit, and the
  read-only live MCP status probe required to build that view. Host plugin and
  marketplace writes use the shared Control command path with
  `configuration_revision` CAS; pure AppConfig mutations stay CAS-only, while
  install and marketplace fetch report external-effect stage from the actual
  effect path and recover from operation-attributable receipts without replaying
  those effects. Managed cache roots are content-addressed and never overwrite a
  published tree still usable by an active Runtime. Production git sources
  allow only HTTPS and authenticated SSH; staging/content-hit cleanup is
  bounded. Assembled Runtimes pin the exact managed content they use; lifecycle
  release removes the pin and reference-aware GC reclaims content absent from
  both current configuration and every live Runtime. Committed Plugin changes
  affect later Session activations and never replace an active Runtime.
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
- `control/client/httpclient`: the authenticated HTTP implementation of the
  complete focused AppServer client set. In-process clients bind the same
  contracts directly; transport selection does not change surface semantics.
- `app/controlserver`: the Control Host's HTTP handler and production listener,
  including authentication, request policy, TLS, token-file policy, and
  shutdown drain. It maps the complete focused AppServer protocol and
  independent Task observation protocol and depends
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
  Session ID. A stateless workspace assembler reads one complete app
  configuration document plus workspace files on the first execution after a
  Session has no live activation, then the detached Session Runtime keeps that
  context-shaping composition fixed until release. Durable Session creation,
  inspect, and reconnect allocate no execution Runtime. An explicit reconnect
  continuation does hold one process-local observation reference: multiple
  clients may observe the same Session, while a selected TUI/GUI Session keeps
  its Runtime resident without claiming execution ownership. An accepted Turn
  or durable running Task keeps the Runtime resident even with no observers.
  Once work is idle and the last observation closes, Control releases the
  Runtime; the next work-bearing activation naturally assembles current
  configuration. This is reference counting plus running state, not another
  lease, timer, persisted generation, or user-facing reload command. Prompt,
  skill, plugin, tool, sandbox, Agent, and placement changes therefore affect
  later activations rather than mutating a live Runtime. The App model catalog is a
  deliberate exception: `/connect` and `/model del` are immediately visible to
  every model picker, while `/model use` writes either the selected Session or,
  when no Session is selected, the persisted Host default. A live Runtime pins
  every model it has selected so deleting a catalog entry cannot interrupt it.
  When a dormant Session reconnects, a missing durable model reference is
  revision-safely repaired to the current default (or cleared when no model is
  configured) before the reconnect snapshot is returned.
  No workspace configuration generation or Session-to-generation binding is
  persisted; durable Sessions store only canonical workspace identity.
  Session activation and app configuration mutation are independent: assembly
  needs no Host-wide or cross-process lease because it builds only from the
  document it read. Configuration writers use the configuration store's short
  atomic compare-and-save boundary; current-schema readers consume a complete
  atomic-replacement snapshot without taking the writer lock. Only legacy
  migration and the bounded Windows non-atomic fallback enter a read-side lock.
  Writers do not scan or rewrite already activated Session state. Runtime
  release first hides its Runtime from routing, waits already-routed synchronous
  mutations and Runtime producers, and shutdown drains in-flight assembly and
  release before closing all Session Gateways and the Host composition. Product
  topology is one live Control Host per store. A bare
  TUI, Headless, or product ACP launch discovers the user-private local Host,
  executes the idempotent local service-start convergence when absent or when
  its own build is eligible to replace the selected service, waits for
  readiness and exact build identity, then binds focused HTTP/SSE clients.
  Presentation exit does not stop that Host; `caelis service
  start|stop|restart|status` provides explicit authenticated lifecycle control
  (`svc` and `gateway` are compatibility aliases) without an attachment lease
  or connection-count authority. Release selection is monotonic by the full
  `caelis` distribution version, while development selection replaces on
  exact BuildID mismatch inside its isolated default Store; unstamped or dirty
  development binaries derive that identity from executable content. Protocol,
  Envelope, API, and capability compatibility remains a Surface-owned policy
  independent of distribution selection. `--embedded` is the explicit
  single-client in-process exception; its built-in ACP children use a private
  authenticated loopback adapter to attach that same Host. An explicit
  Control URL attaches a
  caller-selected Host and never falls back to managed local mode. Session
  lifecycle, main-Turn ingress, and Agent-message delivery use principal-bound
  AppServer clients over embedded or HTTP/SSE transports. TUI and product ACP
  slash routers also use
  focused status, configuration, Agent, participant, completion/skill, and
  plugin clients; slash parsing and display stay client-side. The production TUI
  facade contains no Runtime, Stack, or embedded compatibility Adapter. Product
  ACP direct-command discovery reads the same fixed Session Runtime placement
  snapshot and does not activate an idle Runtime. Shared state directories are
  durable truth only and are never a substitute for live Host authority.
  Host/workspace status, completion, model/sandbox configuration, Agent
  catalog/connection, and plugin operations are authorized by the bound
  AppServer capability and do not require or create a Session. A selected
  Session may be supplied only as optional projection or best-effort binding
  context. A client without a selected Session explicitly addresses its
  workspace by key and canonical working directory; the persistent Host's
  startup workspace is never an implicit substitute for that client address.
  `UserID` remains a deprecated compatibility persistence field, is
  not a Runtime partition key, and must not be introduced into new reusable SDK
  contracts; identity and authorization migrate outward to Control principals.
- `internal/kernel`: Control-owned Session/Turn coordination, gateway
  contracts, and their current implementation. The contracts formerly exposed
  by `ports/gateway` now have one authority here rather than a forwarding port.
- other `internal/control*` packages: current Control integration
  implementations that may converge with adjacent `app/*` and `control/*`
  ownership before any later package split.
- `app/gatewayapp/controladapter`: narrow server-side assemblers for existing
  Control semantics plus the presentation-facing typed-client facade. The
  production facade contains clients only; the broad `Adapter` facade and its
  compatibility constructors have been removed, and tests exercise either the
  same narrow assemblers used by AppServer services or typed clients. Do not
  add product-client operations to the private
  `internal/controlprompt.Service` aggregate or recreate `ports/*`; stable
  capabilities belong in coherent `control/*` packages. The TUI uses external
  ACP connections only as Side ACP participants; it does not bind an external
  ACP endpoint as the Session's main controller or project that endpoint's
  slash/model catalog. A legacy ACP-controller Session cannot be resumed or
  activated for work by the TUI. This presentation boundary is separate from
  Caelis serving inbound ACP clients, whose model selection uses the ACP model
  channel rather than a `/model` command.
- `internal/acpagentbridge`: external ACP transport, process-lifecycle, and
  product integration adapters that make external endpoints implement the same
  SDK controller/participant contracts used by built-in Agents. Product
  assembly supplies principal-bound Session, Agent-message, presentation,
  terminal, participant, Task, status, configuration, Agent, completion, and
  plugin clients; prompt, slash, and Agent-message operations fail closed on
  those clients. Agent-message Turn observation is reconstructed from the
  authoritative Session feed and never exposes a Kernel Turn handle. The direct
  Runtime adapter remains only for lower-level bridge conformance and
  is not selectable through product `GatewayAgentConfig`.
  The bridge does not import presentation packages.
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
- canonical `EventTypeContext` plus typed `Event.Actor` for Agent-authored
  messages that must not impersonate User input;
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

Agent-to-Agent delivery uses one target-owned canonical Context path.
`SendMessage` routes parent, child, and sibling messages; local target delivery
persists before wakeup and does not create a second mailbox truth or reuse
`Task write`. Parent-side child-directed audit is an accepted-delivery mirror,
not main model context. Acceptance means the delivery runner owns the queued
work; it does not mean the target consumed the message. Agent-message projection requires explicit message
metadata or a maintained Agent-message source in addition to typed Actor
identity. After delivery ownership transfers, failure to refresh the sender's Task index
returns `accepted_unpersisted` rather than a retryable delivery error.
Completion notices carry only state and the Session-scoped Task handle, leaving
final output under the Task owner. Task read/wait advances a single observation
frontier and returns all unread retained per-Turn FinalResponses without
repeating an already observed Turn. They are best-effort hints after authoritative
Task/sidecar completion and cannot delay the terminal producer. Task observers follow later message-authored
child Turns with the same absolute cursor without advertising Task input.
The Task stream subscription declares whether it follows the complete subagent
timeline. A following observer releases each completed activity's producer
observation, waits on the stable Session/Task identity, and re-resolves the
producer when the child starts another activity period. The absolute
event/output frontier is persisted
with the Task so replacement or rehydration cannot reset cursor numbering.
Opening a cold terminal child workspace loads history on demand from its
durable child Session. Provider-owned Sessions use a read-only ACP
`session/load` selected by the Task's frozen placement; Task final responses are
observation slices and never serve as transcript history.
Command Tasks and non-following subscriptions still stop at terminal state.
`SendMessage` output is only the delivery acknowledgement; it does not manage
subscriptions. Likewise, `turn_id` groups transcript events and never owns the
Task or presentation lifecycle.

A Spawn-created child is a stable identity with running and idle activity
periods; a terminal Turn outcome never makes that identity unrecoverable.
SendMessage may reconnect the same child Session and start its next Turn.
Cancel ends only the current Turn and is reserved for explicit stop or
prolonged lack of progress. Control tool assembly never gives a Spawn-created
Session the Spawn tool, so delegation cannot nest.

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
