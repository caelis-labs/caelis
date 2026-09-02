# Caelis Architecture

Caelis follows one dependency direction:

```text
Presentation Surfaces -> Control -> Agent Runtime / SDK
```

Physical package movement is not an architecture goal. Ownership follows
semantics, and each capability has one authoritative data path.

## Layers

| Layer | Owns | Must not own |
| --- | --- | --- |
| Surfaces | TUI, headless, and ACP presentation; input collection | Model, tool, sandbox, policy, persistence, replay, permission, or lifecycle decisions |
| Control | Product configuration, Agent assembly, placement, Session and endpoint lifecycle, permissions, replay, orchestration, controller selection, and handoff | Presentation layout or reusable provider/runtime mechanics |
| Agent Runtime / SDK | Reusable model, tool, Session, sandbox, policy, task, subagent, and Runtime contracts and implementations | Caelis product configuration, Host composition, product wire transport, or presentation |
| Host-private composition | Process startup, concrete services, transports, credentials, stores, Runtime activation, and shutdown | A second product semantics API |

ACP is the interoperability language shared by built-in and external Agents.
`acp-go-sdk` owns standard wire contracts and connections. Reusable normalized
semantics belong in the SDK; Caelis projection, compatibility, and permission
translation stay with the focused Control, Host-private, or Surface owner that
uses them.

## Documentation owners

- This file owns the layer, repository, Host, and current-state map.
- [Agent SDK Boundary](agent-sdk-boundary.md) owns reusable Runtime, durability,
  concurrency, tool, and instruction-authority contracts.
- [ACP Projection Contract](acp-projection-architecture.md) owns Envelope,
  live/replay, Task, and Surface projection rules.
- [External ACP Agents](external-acp-agents.md) owns onboarding, authentication,
  model selection, endpoint compatibility, and disconnect behavior.
- [Testing](testing.md) and [Release](release.md) own their procedures.

Implementation details belong in package comments and tests. Completed migrations
and acceptance history belong in Git and CI, not in this map.

## Repository map

| Path | Responsibility |
| --- | --- |
| `cmd/caelis`, `internal/cli` | Process entry, mode selection, local Host discovery/start, explicit remote attach, and embedded fallback |
| `surfaces/tui`, `surfaces/headless`, `surfaces/acp` | Concrete presentation and transport-facing Surface behavior |
| `surfaces/internal/*` | Private shared presentation components, not product APIs |
| `control/appserver` | Principal-bound aggregate product-client contract, Session feed/replay, lifecycle write gate, commands, approval recovery, and operation ledger |
| `control/appserver/eventstream`, `projection`, `wirev1` | Typed Envelope vocabulary, canonical projection, and versioned HTTP/SSE schema |
| `control/taskstream`, `control/appserver/taskstream` | Session-authorized Task directory, observation, and Task-to-Envelope projection |
| `control/modelcatalog`, `modelconfig`, `modelprofile`, `placement`, `agentbinding` | Provider and model discovery, credentials/configuration, selectable profiles, placement, and fixed Agent bindings |
| `control/agents` | External ACP Agent identity, preparation, connection, and configuration |
| `control/memorybinding` | Opaque host-selected Memory binding references, Runtime actor and audience delegation, and immutable logical snapshots |
| `control/mcpconfig`, `control/plugin`, `control/status` | MCP assembly inputs, plugin lifecycle, and product status read models |
| `app/controlserver` | Authenticated HTTP/SSE Host listener, policy, readiness, and drain |
| `app/gatewayapp` | Product Host composition, Session Runtime registry, concrete Control services, and shutdown |
| `app/gatewayapp/controladapter` | Host-private server assembly; `local.NewAppServer` is the sole production root that receives a concrete `gatewayapp.Stack` |
| `internal/kernel` | Product Session/Turn coordination and main Runtime integration |
| `internal/controlclient/turningress` | Private main-Turn ingress to the Control Session feed |
| `internal/controlprompt`, `internal/controlprompt/appserveradapter` | Private prompt parsing and adaptation over focused AppServer clients |
| `internal/controlassembly`, `internal/controlplane`, `internal/acpagentbridge` | Private Agent assembly, endpoint/handoff coordination, and external ACP integration |
| `agent-sdk/*` | Reusable SDK package tree in the root Go module |
| `platform/*` | Platform-specific product support |

Stable product capabilities belong in coherent `control/*` packages. Concrete
composition remains private under `app/*` and `internal/*`; those paths are not
a second supported product API.

## Product Host and clients

There is one live Control Host per Store. A normal TUI, headless, or product ACP
launch discovers or starts the managed local Host and binds authenticated
AppServer clients. `-control-url` selects an explicit Host. `-embedded` is the
single-process exception; automatic embedded fallback is allowed only after
proving that no Host owns the Store and managed startup failed.

Presentation exit does not stop the managed Host. Host lifecycle is explicit
through `caelis service start|stop|restart|status`. Shared files are durable
truth, never a substitute for live Host authority.

`control/appserver.AppServerClients` is the aggregate capability boundary used by
product clients. Focused Control packages remain the semantic owners behind it.
Task observation is independently delivered and never joins the Session feed, so
a slow Task observer cannot delay or reclassify the parent Turn.

Host-private adapters depend on focused services. They must not retain a concrete
`gatewayapp.Stack`, a Runtime lease, the root Runtime composition, or bound Stack
methods. `Stack` is the process composition root, not a reusable service locator.

## Session Runtime lifecycle

The Host owns process-lifetime authorities and a Session Runtime registry keyed by
durable Session ID. Creating, inspecting, listing, or reconnecting a Session does
not by itself assemble execution state.

The first work-bearing activation samples one complete AppConfig and workspace
snapshot and builds an independent Runtime composition. That snapshot stays fixed
while the activation lives. Accepted work and durable running Tasks keep it
resident; observation may retain it after activation, but never grants execution
authority. Once work is idle and the last observation closes, Control releases
the Runtime. The next activation samples current configuration.

Ordinary model, skill, MCP, plugin, sandbox, and placement changes therefore
affect later activations. Explicit provider or ACP-Agent removal is the narrow
revocation exception: Control removes the profile from live placement and repairs
affected Session bindings so later work cannot resolve a deleted endpoint.

Memory is a default-enabled built-in Host capability. Process composition
synchronously opens the imported `github.com/caelis-labs/memory/appliance` Go
package and applies its schema migrations. Successful Host construction means
Memory is available; there is no independent install, process, endpoint,
readiness, compatibility handshake, or degraded Host state. First Host startup
atomically provisions one private Identity, Space, View, Grant, issuer
credential, and default binding. These are internal topology and authority
records, not setup fields.

Memory then follows the same activation boundary. Control selects one opaque
`BindingRef` internally, then detaches only that binding's Runtime actor,
principal, issuer reference, View, Grant, single `private` or
`shared` audience, and binding version. Caelis additionally derives one mandatory
opaque Label from the canonical workspace key by SHA-256. The exact LabelSet is
bound into each issued capability, so identical terms from different workspaces
cannot cross Recall, receipt, or consistency-cursor boundaries. Raw workspace
paths and labels never enter the model-visible tool schema or result.

The activated Runtime does not retain the complete binding catalog or any
downstream product identity. A future product layer may map a Bot, user, tenant,
or another concept to a `BindingRef` or append opaque labels through the
embedding-only selector; Memory still sees neither those product concepts nor
their semantics. The mandatory workspace label cannot be removed by that
extension. Later binding changes affect later activations. A new canonical
Session pins its complete non-secret delegation and LabelSet at creation. A
Session created before Memory was enabled is pinned before its first Memory call
under the Runtime fence. Public or mixed-audience Runtime composition is invalid.

The current Session pin includes binding, actor, principal, issuer reference,
audience, View, Grant, binding version, and the canonical LabelSet. A legacy
pre-LabelSet Session pin is upgraded at its first post-upgrade admission and its
old empty-partition consistency cursor is discarded; after that admission its
labels cannot change. Historical schema-v2 AppConfig `bots` records are
atomically rewritten to opaque bindings when exactly one identity exists.
Multiple historical identities are rejected because silently selecting one
would change authority; there is no Memory lifecycle switch or partially active
Host state.

Host-private composition binds the Memory SDK directly to the embedded
`DataPlane`. Runtime capabilities are issued and renewed from an owner-only
issuer credential store; the model sees exactly `Remember(text)` and
`Recall(query)`. Their canonical ToolResults are ordinary Session history, so
replay reads the stored bytes and does not repeat a Memory call. Consistency
cursors and provenance references remain in model-hidden Session state and
ToolResult metadata.

The only ordinary user choice is the `Memory Steward` row in `/subagent`.
Without an explicit provider-model binding, Memory keeps its baseline durable
receipt journal and lexical recall path and Caelis never invokes a model for
Memory. Binding that fixed system Agent enables the provider-neutral Steward
Worker callback: Memory supplies bounded evidence and appliance-owned prompt
policy, Caelis runs the selected existing provider model with no tools, and
Memory validates and applies the returned proposal. Steward deliberately has
no default-profile fallback, so an absent binding is a stable zero-token mode.

Shutdown closes admission, cancels and drains producers, waits for routed
mutations and Runtime cleanup, then closes stores and process resources.

## State, streams, and effects

`agent-sdk/session.Event` and guarded Session state are durable truth. Canonical
messages, tools, plans, protocol facts, and journals determine model context and
recovery. UI, overlay, notice, and raw observation output are transient unless a
typed contract explicitly says otherwise.

The Control-owned Session feed provides ordered live and replay delivery through
opaque Envelope cursors. Typed Envelope fields own identity, relation, scope,
position, approval, and resume. `_meta` is display, diagnostics, or bounded
compatibility data and cannot grant authority or repair identity.

A canonical Turn holds the Session execution fence for its complete asynchronous
producer lifetime. Observation and authorized mid-Turn input do not take that
fence. Only a Host admitted by the process-lifetime ownership guard may replace
an abandoned prior-Host fence. Fence conflicts never retry through an unfenced
path.

External effects either use stable identity and explicit recovery or are made
safe to retry as a fresh operation. Managed plugin materialization uses private
staging, immutable content-addressed publication, revision-CAS configuration,
and later cache reclamation. It has no operation-specific recovery receipt: a
failed install or marketplace update is retried with a new operation and the
current configuration revision.

### Store layout

The user Store separates durable semantics from runtime coordination and
content assets:

```text
config.json                 canonical product configuration
control/control.sqlite      Control operation and ACP preparation state
control/cursor.key          private cursor-signing secret
sessions/                   canonical Session documents and event JSONL plus derived SQLite indexes
providers/                  private provider credential material
memory/credentials/         owner-only Memory issuer credentials behind opaque references
memory/appliance/           embedded Memory package data and SQLite authority
plugins/                    installed and marketplace content caches
runtime/service/            live Host discovery, authentication, and ownership files
logs/, updates/, skills/    diagnostics, update state, and prompt assets
```

`control/control.sqlite` is one physical database with separate domain tables.
`control/appserver` owns the operation ledger, while Host-private Gateway
composition owns ACP preparation state. Indexed SQLite columns are checked
against the complete stored record before cleanup.

Session JSONL remains canonical ordered history; `sessions/.sessions.index.sqlite`
is an SDK-owned secondary index and is not a Control store. Credential bytes,
cursor keys, runtime locks and tokens, diagnostic logs, and immutable plugin
content also remain outside the Control database because their lifecycle or
security boundary is different.

An upgrade starts a new Control operation epoch. Retired `control-operations`,
`acp-preparations`, and plugin operation-receipt directories are not read,
imported, or allowed to participate in Host startup. They are disposable legacy
state, while current Session history and product configuration remain intact.
The former root-level cursor key is different: it authenticates live replay
cursors and is atomically moved to `control/cursor.key`.

## Configuration and trust

AppConfig is the canonical product configuration document. Credential bytes stay
in the credential store and AppConfig retains opaque references. Configuration
writes use revision-aware atomic replacement; readers observe a complete document.

Native MCP configuration is assembled from AppConfig plus supported user
overlays. Project `.agents/mcp.json` or `.mcp.json` files are sampled only after
the exact canonical workspace is trusted. Overlay changes never hot-reload an
active Runtime.

Plugin configuration is also canonical AppConfig state. Managed plugin content
is immutable and pinned while an active Runtime uses it; configuration mutation
and installation effects remain on the principal-bound Control command path.

Memory AppConfig contains one default `BindingRef` and opaque actor, principal,
issuer-credential, View, Grant, and
audience values for each binding. Raw
credentials, capabilities, receipts, records, indexes, lifecycle policy, and
Steward state belong outside AppConfig.
References do not grant access: Host-private composition resolves the issuer
credential, while the Memory package remains the authority for
Views, Grants, capabilities, receipts, retrieval, and lifecycle.

Normal product startup exposes none of the binding, data path, or credential
fields. The CLI does not accept Memory lifecycle or topology from the user
environment.
Future product concepts may select another opaque binding through the existing
Host callback without making Bot, tenant, workspace, or similar concepts part
of the Memory API. A future standalone Memory distribution is an independent
ecosystem adapter and cannot become a dependency of the embedded Caelis path.

## Compatibility and current limits

- The root `protocol/acp/*` and product-host `ports/*` trees are retired and
  guarded against recreation.
- Sessions written before v0.42 may retain historical workspace-key aliases for
  the same canonical CWD. Keep this reader until the minimum supported upgrade
  source is v0.42 or newer.
- `UserID` remains a deprecated persistence compatibility field. It is not a
  Runtime partition key and must not enter new SDK contracts.
- External ACP compatibility readers and their removal conditions are listed in
  [External ACP Agents](external-acp-agents.md).

The current product does not provide automatic presentation reconnect after a
Host replacement, a live multi-Session activity catalog, GUI presentation, a
system Bar, or a Pet. Those absences are limitations, not parallel architecture
plans.

## Change rules

- Prefer a focused ownership improvement over a broad rewrite.
- Remove superseded paths and documentation when a replacement lands.
- Give every compatibility path an owner, enabled condition, and removal event.
- Do not create Surface-private replay, permission, persistence, or Runtime paths.
- Do not add a deterministic SDK workflow graph or an Agent-authorized handoff.
- Enforce deterministic dependency rules in architecture checks and prove
  lifecycle, persistence, and replay semantics through owning tests.
