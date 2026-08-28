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
  GUI. Surfaces consume ACP-style `control/appserver/eventstream.Envelope`
  payloads plus
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
ACP adapters. Standard wire contracts and connections come from
`acp-go-sdk`. The former root `protocol/acp` tree has retired: ACP-shaped
product envelopes and permission translation live in Control, rendering lives
in Surface-private projection, and provider compatibility lives at the
Host-private adapter boundary. Canonical runtime events still carry model and
tool truth below that protocol view. More detail lives in
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
- `surfaces/tui`, `surfaces/headless`, `surfaces/acp`: concrete presentation
  entities. Product ACP owns its stdio transport adapter, connection-facing
  Agent dispatch contract, per-connection updates and permission callbacks,
  and slash-result formatting here. Its thin constructor forwards the complete
  aggregate AppServer clients to Host-private ACP bridge assembly; it does not
  select Session authority, assemble the prompt router, or receive a Host,
  Runtime, Kernel, persistence, or assembly handle.
- `surfaces/internal/promptview`, `surfaces/internal/statusbar`,
  `surfaces/internal/transcript`: private shared presentation projection used
  by concrete Surfaces. These packages are not independent product surfaces or
  application-layer entry points.
- `protocol/acp/*`: retired and guarded against recreation. Standard method
  identities, wire contracts, and connection behavior come from `acp-go-sdk`;
  Caelis compatibility metadata is private to the Control, Host, adapter, or
  Surface owner that produces or consumes it.
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
  API-key material follows canonical ModelProfile reachability through its
  provider endpoint. Deleting one of several profiles sharing a credential
  retains it; deleting the final profile first persists a secure exact-byte
  retirement receipt, then removes the source under the reference lock before
  the AppConfig CAS. AppConfig persistence never falls back to an in-place
  truncate when atomic replacement fails, so a rejected CAS leaves the previous
  canonical document readable and restores the prior source before readers
  resume. Startup and later model operations reconcile an interrupted receipt
  against canonical reachability while holding the same reference locks, and
  never overwrite a newer replacement. `/connect` reuses credentials only
  through a still-reachable endpoint. Session Runtime activation pins only
  canonically reachable API-key sources, plus an explicit process-local Session
  pin, from a revision-stable AppConfig/credential observation into a
  Runtime-owned process-local snapshot; Control extends that snapshot only when
  a later model pin commits and copies child Session pins from it. Host
  credential retirement therefore cannot interrupt work that already owns a
  Runtime reference.
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
- `control/taskstream`: the Control-owned, Session-authorized Task directory,
  lightweight current-status observation, and transient content-record
  service for existing Tasks. A content stream is addressed only by
  `(SessionID, TaskID)`, while the directory exposes the Session-unique public
  Task handle used by people, models, and Task control. Surfaces resolve that
  handle or its typed parent-tool relation through the directory before using
  the opaque TaskID; they never recover identity from `_meta`. Directory
  snapshots are complete and replaceable, retain no Task content, and exist
  only while at least one observer watches the Session. This adds no Execution
  lifecycle, durable output store, or schema.
  `control/appserver/taskstream` is the Control AppServer adapter that projects
  those records into transient Envelopes for Surfaces; its HTTP/SSE codec lives
  with the AppServer wire implementation in `control/appserver/wirev1`.
- `control/agents`: external ACP connection identity and lifecycle. Host-scoped
  prepare, explicit authentication, and connect operations use the shared
  Control command ledger; a secret-free, expiring preparation record binds
  discovery evidence to the principal and exact command receipt. One stable
  Agent represents one connection; sibling remote models are separate
  `ModelProfile` entries and never become synthetic Agents or Agent-owned
  defaults. Live ACP Session IDs remain execution state and are never persisted
  as discovery configuration. Authentication recovery, Agent-default behavior,
  and external endpoint installation ownership are defined in
  [External ACP Agents](external-acp-agents.md).
  The configured Agent catalog entry is the canonical read projection consumed
  by Host adapters; adapters do not mirror its name and description fields.
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
  affect later Session activations and never replace an active Runtime. A
  detached Runtime may read its pinned Plugin view but cannot obtain a mutable
  Plugin host over its hook-free configuration reader; writes remain on the
  principal-bound Host command path.
- `control/acppermission`: the focused Control adapter between standard ACP
  `request_permission` payloads and normalized SDK approval semantics. It owns
  no approval decision, display classification, persistence, or endpoint
  policy.
- `control/appserver`: the canonical surface-facing, transport-neutral Control
  boundary. It owns trusted principals, the aggregate client and service
  capability sets, commands and outcomes, Session authorization,
  list/bootstrap/reconnect state, feed/replay coordination, legacy-child-mirror
  filtering, approval recovery, the aggregate client, the durable idempotency
  operation ledger, and the Session lifecycle write gate. Its
  `eventstream.Envelope` package owns the Control-to-Surface feed vocabulary,
  including its ACP-shaped update union and permission payload;
  the shared projection, authorization, state, and broker remain with their
  focused Control owners. Its
  aggregate includes Task observation as an independently delivered typed
  capability; Task lifecycle, cursor, and stream ownership remain separate.
- `internal/controlclient/turningress`: private main-Turn ingress glue between
  `internal/kernel` handles and the Control-owned Session feed. Task output
  never fans into this ingress and cannot delay its terminal.
- `control/status`: Control-owned product status read model shared by client
  and prompt surfaces without presentation formatting. Its nested sandbox
  `Setup` and named checks are the sole setup-state source; adapters normalize
  any transitional host fields before constructing this read model. Structured
  `SessionUsageTotal` and `SessionUsageByModel` values are likewise the sole
  cumulative Session-usage sources; the public model carries no flat mirrors.
  Effective per-Session Runtime state and diagnostics selection are canonical
  Control read inputs here rather than duplicated Host-adapter structs.
- `control/appserver/wirev1`: the versioned, AppServer-bound HTTP/SSE schema
  bindings, generated types, and strict JSON/Envelope codec. Because the codec
  serializes `control/appserver` domain values directly, it stays with that
  semantic owner instead of pretending to be a product-neutral protocol.
- `control/appserver/httpclient`: the authenticated HTTP implementation of the
  complete AppServer client set, including Task observation. In-process clients bind the same
  contracts directly; transport selection does not change surface semantics.
- `app/controlserver`: the Control Host's HTTP handler and production listener,
  including authentication, request policy, TLS, token-file policy, and
  shutdown drain. It maps the complete AppServer capability set while keeping
  Task observation on its independent protocol and depends
  on explicit Control contracts rather than `gatewayapp.Stack`.
- `internal/controlprompt`: current Control-owned surface-neutral prompt input
  contract, private prompt facade, command catalog, parsing helpers,
  connect-wizard state, and shared slash orchestration. It is not a product
  client API and must not accumulate transport or presentation semantics.
- `internal/controlprompt/appserveradapter`: private client-side adaptation
  from the aggregate `control/appserver.AppServerClients` capability set to the
  prompt facade consumed by concrete Surfaces. It contains no Host, Runtime,
  Kernel, persistence, or transport authority and is not a second product API.
- `internal/controlassembly`: product Agent assembly resolution.
- `internal/acpagentbridge/assembly`: private external-ACP ControlPlane
  composition. One shared registry backs controller and subagent execution;
  registry replacement remains a `ControlPlane` operation rather than a
  mutable cross-package updater authority.
- `internal/controlplane`: shared-ledger routing, endpoint lifecycle/recovery,
  and handoff coordination.
- `app/gatewayapp`: the current product Control host and composition entry. Its
  app-scoped Session Runtime registry routes public Control execution by durable
  Session ID. The registry is constructed from explicit process authorities and
  a narrow assembler contract; it does not retain the Host `Stack` or root
  Runtime composition. It owns the activated Runtime set, reference counting,
  release, and collective shutdown drain. Registry entries hold private
  `sessionRuntimeInstance` values rather than child Host `Stack` values. Each
  instance owns an independent pinned Runtime composition and disposable
  workspace resources while borrowing only the focused process authorities
  required for execution. The stateless workspace assembler retains an explicit
  value of those authorities, a dedicated configuration store instance without
  Host write hooks, the immutable startup migration report, and the Host's sole
  mutable process-configuration source. The root Runtime's `activeRuntime` is
  an installed execution artifact rather than a second publication target; a
  process model, sandbox override, skill-root, or child-control change is
  published once to the process source. On the first execution after a Session
  has no live activation, the assembler samples that source once and reads one
  complete app configuration document plus workspace files. Plugin
  configuration comes only from that canonical document. The detached Session
  Runtime keeps the resulting context-shaping composition fixed until release.
  Durable Session creation,
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
  migration enters a read-side lock.
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
  single-client in-process exception. A bare launch also falls back to that
  mode only when the initial lifecycle probe proves no Host owns the Store and
  the managed service cannot start; an existing Ready or Unreachable Host is
  never bypassed. Embedded built-in ACP children use a private authenticated
  loopback adapter to attach that same Host. When the environment forbids even
  that private listener, the main in-process client remains available while
  cross-process built-in children are unavailable. An explicit
  Control URL attaches a
  caller-selected Host and never falls back to managed local mode. Session
  lifecycle and main-Turn ingress use principal-bound
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
  Product clients derive the key from that canonical directory. Sessions
  written before v0.42 may retain another key selected by the retired CLI
  override; Control lists those compatibility aliases by canonical CWD and
  exact Session resume retains the durable key. One key may never identify two
  directories. HTTP clients require the `workspace-cwd-session-list-v1`
  capability before sending this CWD-scoped read, so an older remote Host
  cannot silently interpret it as an unscoped list. Keep this reader until the
  minimum supported upgrade source is v0.42 or newer.
  `UserID` remains a deprecated compatibility persistence field, is
  not a Runtime partition key, and must not be introduced into new reusable SDK
  contracts; identity and authorization migrate outward to Control principals.
- `internal/kernel`: Control-owned Session/Turn coordination, gateway
  contracts, and their current implementation. The contracts formerly exposed
  by `ports/gateway` now have one authority here rather than a forwarding port.
  Reusable approval-state and Session-usage semantics remain owned by
  `agent-sdk/*`. Kernel emits only its private legacy presentation fallback;
  typed Envelope relation fields remain authoritative and no shared ACP
  metadata facade sits between Kernel and its consumers.
- other `internal/control*` packages: current Control integration
  implementations that may converge with adjacent `app/*` and `control/*`
  ownership before any later package split.
- `app/gatewayapp/controladapter`: narrow Host-private, server-side assemblers
  for existing Control semantics. Its `local` subpackage is the only production
  package that consumes the root adapter package directly and assembles the
  complete in-process AppServer service set. `local.NewAppServer` is the sole
  production composition root that receives a concrete `gatewayapp.Stack`. It
  selects focused Host services and, for Session-bound requests, focused
  services from an authorized Runtime lease. Leaf services retain neither the
  Stack, the lease, nor a wide union of Runtime capabilities. Exact model,
  Agent catalog, Session Runtime, and
  diagnostics-selection reads use their canonical `control/*` owners, while
  deliberate Sandbox and Doctor subsets remain named Host-to-Control
  projections. The root package does not import the concrete Host. Every other
  production package
  depends on `control/appserver`, a focused Control contract, or the composed
  `local` adapter; the root adapter package is not a product-client API. The
  exported constructors and projection types under `app/gatewayapp` and its
  adapter packages exist for repository-internal assembly, not downstream Go
  source compatibility. `control/appserver` and focused `control/*` contracts
  are the supported product boundaries.
  Production `app/*` code does not import `surfaces/*`. Do not add
  product-client operations to the private `internal/controlprompt.RouterService`
  or recreate `ports/*`; stable capabilities belong in coherent `control/*`
  packages. The shared router consumes only prompt-routing facets; the TUI owns
  its additional mode, completion, plugin, connector, and binding aggregate.
  The TUI uses external
  ACP connections only as Side ACP participants; it does not bind an external
  ACP endpoint as the Session's main controller or project that endpoint's
  slash/model catalog. A legacy ACP-controller Session cannot be resumed or
  activated for work by the TUI. This presentation boundary is separate from
  Caelis serving inbound ACP clients, whose model selection uses the standard
  ACP `model` configuration option rather than the retired `models` response
  field, `session/set_model`, or a `/model` command. The external-Agent client
  retains the old model channel only as a compatibility fallback for peers that
  do not advertise a model configuration option.
- `internal/acpagentbridge`: external ACP transport, process-lifecycle, and
  product integration adapters that make external endpoints implement the same
  SDK controller/participant contracts used by built-in Agents. Product
  assembly supplies principal-bound Session, presentation,
  terminal, participant, Task, status, configuration, Agent, completion, and
  plugin clients; prompt and slash operations fail closed on those clients.
  The product gateway assembly selects the authorized ordinary or system
  Session client for each target and owns prompt-router and dynamic-command
  composition; the Surface injects only its plain-text slash formatter.
  Child input uses the reusable Runtime capability and standard ACP prompt or
  steering methods; Task observation is derived from child output and never
  exposes a Kernel Turn handle. The direct
  Runtime adapter remains only for lower-level bridge conformance and
  is not selectable through product `GatewayAgentConfig`.
  The bridge does not import presentation packages.
- `platform/*`: product support code for platform-specific host behavior.

## Stable Skeleton and Composition Assets

The stable skeleton is defined by semantic ownership, not by a package named
`core`. It has three categories with different compatibility expectations:

| Category | Current owners | Contract |
| --- | --- | --- |
| Reusable Runtime skeleton | `agent-sdk/*` | Runtime, Session, model, tool, sandbox, policy, and Task contracts stay independent of the Caelis product Host and wire implementation. |
| Stable product skeleton | `control/appserver` and focused `control/*` packages | AppServer is the one aggregate capability boundary obtained and validated by product client composition. A concrete Surface receives the aggregate or only the focused members it consumes. Focused Control packages own product state and policy; feature-bound wire codecs and projection stay with their semantic owner. |
| Private lifecycle skeleton | `internal/kernel`, `internal/controlplane`, `app/gatewayapp` | Session/Turn coordination, controller ownership, Host authority, Runtime activation, reference counting, and shutdown ordering are mandatory product invariants, but remain private until their contracts can be separated without changing lifecycle semantics. |

Concrete implementations are composed around that skeleton. They are
replaceable at a declared binding boundary; they do not define another
aggregate product API or take ownership of Control policy:

| Binding boundary | Composed assets |
| --- | --- |
| Host startup | persistence stores, credential providers, operation ledger storage, HTTP/in-process transport, external process launch, and service lifecycle integration |
| Session activation | model/provider client, plugin and skill contributions, sandbox backend, MCP connections, Agent endpoint backend, placement resolver, and approval decision backend |

Replaceable means constructor- or activation-time injection, not mutation of an
active Runtime. A live Session Runtime retains its fixed assembly snapshot until
release. Host-wide authorities remain process-scoped, while request-scoped
identity and idempotency never become fields on a reusable SDK Runtime. The
principal, Session target, expected revision, operation ID, approval response,
and observation cursor are request binding data and Control guards, not
replaceable components.

The current first package-convergence boundary is therefore:

```text
product client composition
    -> control/appserver (obtain and validate one complete aggregate)
        -> internal/acpagentbridge (product ACP Agent assembly)
            -> surfaces/acp (stdio dispatch and connection callbacks)
        -> surfaces/tui (focused prompt clients plus Task observation)
        -> surfaces/headless (focused Session/Turn client)

focused Control authorities
    -> agent-sdk Runtime contracts

app/gatewayapp (Host composition and private lifecycle skeleton)
    -> control/appserver (complete aggregate services)
    -> concrete startup/session components

surfaces/internal/{promptview,statusbar,transcript}
    private presentation implementation only
```

Production `app/*` code must not import `surfaces/*`, and Surfaces must not
import Host, Kernel, or Runtime implementations. The private
`internal/controlprompt/appserveradapter` may translate the focused AppServer
members selected from an already validated aggregate into surface-neutral
prompt operations, but may not become a second capability boundary. The
Host/Session Runtime distinction is a private lifecycle boundary, not a package
extraction commitment: the Runtime registry is constructed without retaining a
concrete Host `gatewayapp.Stack`, and activated Sessions use private
`sessionRuntimeInstance` values. `Stack`, `sessionRuntimeInstance`, and their
shared private `runtimeComposition` implementation remain Host-owned lifecycle
details rather than stable package contracts. `Stack` owns its composition as a
named private field; `runtimeComposition` exports no state. Its Host-only
`runtimeProcessState` and detached `sessionRuntimeActivation` make mutable
process publication and immutable activation selection distinct. Host startup
also names Control-service assembly and Runtime activation as separate private
phases without exposing either as a reusable product API. Only deliberate
focused service getters cross the package boundary. Authorized Runtime leases
remain request-scoped and expose focused service selections rather than a wide
function bag. Reconnect live-state observation, Participant handle projection,
and Task child-history fallback use dedicated private readers instead of Stack
pass-throughs. Full Plugin mutation remains behind the principal-bound AppServer
command service rather than a public Stack getter. The command service delegates
to a private command backend that owns command-scoped state and a once-bound
Runtime registry, not to `Stack` or bound Stack methods. Presentation reads are
normalized by the Host-private `PresentationSource` into `control/appserver`
types before they reach `controladapter/local`; ACP fallback providers are
accepted only as inputs to that normalization boundary. Direct Stack mirrors
of execution, configuration revision, Model, Agent, Status, Runtime acquisition,
workspace and preparation service methods are not parallel
entry points. Architecture and structural gates enforce the private adapter
consumer boundary, reject concrete Stack use in local leaf adapters, reject ACP
wire types in local presentation assembly, reject anonymous Host composition,
freeze deliberate public Stack methods, and prevent Registry, Runtime instance,
assembly, command backend, and focused projection types from retaining a
concrete Host `Stack` or the Host root `runtimeComposition` where prohibited.

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

The ban on importing product `protocol/acp/*` packages from the SDK does not ban ACP
semantics from the SDK. Dependency direction is from standard wire bindings and
product adapters toward reusable SDK contracts, never the reverse.

Package-level ownership and the supported public import set are defined by
[Agent SDK Boundary](agent-sdk-boundary.md),
[`agent-sdk/supported-packages.txt`](../agent-sdk/supported-packages.txt), and
the enforced architecture gates.

Control fences each canonical Session Turn across its complete asynchronous
producer lifetime. Agent-loop safety remains inside the Agent implementation;
one Agent must not inspect or cancel an unrelated external endpoint.

The fence is scoped to the product Host epoch that already owns the Store. It
has no TTL, renewal heartbeat, or client attachment semantics: observers and
mid-Turn input may come from multiple clients without acquiring it. Exact
release ends a producer; after the process-lifetime Host ownership guard admits
a replacement Host, Control may explicitly replace an abandoned prior-Host
fence and receives a higher fencing token. Ordinary Runtime admission cannot
take over another live producer merely by choosing a different owner ID. The
replacement capability pins that Host guard across the Store mutation. A
fenced Runtime must guarantee a producer-completion waiter; only a nil
completion result permits exact release. Ambiguous releases are reconciled by
a fresh read and retried for the remaining Host lifecycle. Acquisition returns
an opaque bearer claim used by mutation guards and exact release; observation
returns identity only, so another client cannot turn a read into producer
authority. A committed acquisition must return that exact claimed result or
the caller fails closed until a later Host epoch performs startup recovery.
Bounded runtime
diagnostics classify slow or failed acquire, producer-start,
release/reconciliation, startup-recovery, and startup-release phases, plus failed producer
completion waits, by elapsed time only;
they never record Session identity, prompt content, paths, or raw error text.

## Durable State

`agent-sdk/session.Event` is the source of truth for persisted runtime context.
Durable model-visible facts require canonical payloads:

- `Event.Message` for model messages;
- canonical `EventTypeContext` plus typed `Event.Actor` for Agent-authored
  messages that must not impersonate User input;
- `Event.Tool` for tool calls and results;
- `PlanPayload` for plan state;
- `EventProtocol{Method, Update, Permission, AgentCommunication}` for
  ACP-compatible coordination
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

Agent-to-Agent communication uses the standard input transport with an explicit
`agent_communication` semantic kind. `SendMessage` routes parent, child, and
sibling addresses without a delivery MessageID, durable mailbox, parent audit
mirror, or `Task write` fallback. Running ACP endpoints use negotiated steering;
idle endpoints use ordinary prompts on the same Session. Runtime persists a
canonical `EventTypeContext` with the trusted source `Event.Actor` and an
explicit Agent-communication protocol payload. Provider APIs still receive a
user-role-compatible prompt, prefixed with that trusted sender identity. Client
projection uses `caelis/agent_communication`, so Surfaces never render the
message as a real User submission. Input admission does not mutate Task.
Completion notices carry only state and the Session-scoped Task handle, leaving
final output under the Task owner. They are one bounded best-effort conversation
submission to the exact active parent Run after authoritative Task/sidecar
completion and cannot delay the terminal producer. Task read/wait advances a
single observation frontier and returns all unread retained per-activity
FinalResponses without repeating an already observed activity. Task observers
may follow later child activities with the same absolute cursor without
advertising Task input. Every Runtime frame and snapshot carries the concrete
ActivityID that produced it; Control prefers that identity over the Task
descriptor captured when a long-lived observer attached, so a later Turn cannot
be mislabeled as the previous activity. Separately, the lightweight Task
directory publishes complete, replaceable Session snapshots when lifecycle,
activity identity, routing identity, or capability state changes. Output-only
Task commits do not wake it. Multiple clients may watch independently, and
Control releases all process-local index state for a Session when its last
directory observer closes.

Content demand remains independent from status observation. A visible child
workspace follows its stable Task identity across running and idle activity
boundaries; when retained Runtime output no longer reaches its cursor,
current-state recovery may begin at a later stable boundary and omit the
unavailable running prefix. Hiding the workspace releases that live observer.
At a terminal lifecycle, the Surface independently issues one finite history
read without replacing the visible live observer. Control reconstructs the
exact child endpoint from the Spawn Task's frozen placement and, when that
endpoint advertises ACP `session/load`, loads the complete child transcript
through the Agent, including user, assistant, reasoning, tool, and lifecycle
updates. This applies equally to built-in and external ACP Agents; Control
never reads a child Session store as a presentation shortcut. The short-lived
ACP transport closes after load and does not resume execution. A
Spawn that failed before creating a child Session remains observable through
retained Runtime current state. An endpoint without `session/load` also uses
that state while its Runtime exists; after Runtime release, Control may expose
the bounded durable terminal Task result and lifecycle as current state. These
compatibility paths make no complete-history guarantee and do not invent a
compaction notice. Task final responses remain observation slices: the bounded
terminal fallback is not transcript history. A cold terminal workspace performs
only the finite history read until the directory observes a later running
activity. Command Tasks and non-following subscriptions still stop at terminal
state. The finite read is fenced by the directory's expected ActivityID before
and after `session/load`; the Surface installs the invisible projection only
when the response and current directory still name that activity. Once a child
Session exists, its frozen endpoint placement is required and malformed routing
fails closed instead of being disguised as a pre-Session fallback. A built-in
history bridge receives a Host-issued process-scoped read capability; the ACP
Surface only forwards that opaque assembly input, while the bridge validates it
together with the durable parent/Task relation. Ordinary product ACP load,
resume, prompt, and Session ownership remain unavailable for managed children.
`SendMessage` output is only the input-dispatch acknowledgement; it does not
manage subscriptions. Likewise, `turn_id` groups transcript events and never
owns the Task or presentation lifecycle.

A Spawn-created child is a stable identity with running and idle activity
periods; a terminal Turn outcome never makes that identity unrecoverable.
SendMessage may reconnect the same child Session and submit its next prompt.
Cancel ends only the current Turn and is reserved for explicit stop or
prolonged lack of progress. Control tool assembly never gives a Spawn-created
Session the Spawn tool, so delegation cannot nest. Spawn-created child prompts
state that they share the parent workspace and CWD. Optional Spawn `handle`
requests a unique Session-scoped Task identity; omitting it keeps Runtime
assignment. Optional Spawn
`include_context` reuses Control's participant `ContextTransfer` (latest compact
checkpoint plus later user messages and Turn Finals) and never copies tool
traces or reasoning.

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
