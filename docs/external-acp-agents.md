# External ACP Agents

This document owns the maintained product contract for local external ACP
Agent onboarding, authentication recovery, default model behavior, and the
checked-in ACP Registry snapshot. Layer ownership remains defined by
[Caelis Architecture](architecture.md).

## Onboarding Commands

External ACP onboarding has three Host-scoped, principal-bound AppServer
commands. `prepare` records a durable intent before launcher installation,
process startup, Session creation, or authentication can begin. It returns an
opaque preparation reference and exact content digest in the command receipt.
If the Agent requires authentication, the preparation becomes `needs_auth` and
publishes only wire-safe method identities; the initiating presentation layer
must choose one exact method and invoke `prepare-auth` as a second idempotent
command. A ready preparation contains normalized discovery evidence and may be
consumed by `connect`, which commits the external Agent and ModelProfile with
the expected Host configuration revision.

Preparation records are secret-free, principal-owned, time bounded, and not a
second operation ledger. They allow an intent-only operation receipt to be
recovered observationally after restart without repeating launcher, process,
authentication, or Session effects. Cleanup that cannot be proven produces an
unknown outcome and is never retried blindly. `connect` uses configuration
compare-and-save and canonical roll-forward; a committed write is not undone
when assembly refresh or durability observation later reports a warning.
Within the single Control Host, an exact-operation gate spans intent creation,
effect execution or observational recovery, and receipt completion. Restart
recovery uses the durable intent plus the preparation record; it does not rely
on a cross-process execution lease. A concurrent exact duplicate therefore
cannot observationally recover a terminal preparation before the original
creator persists its exact receipt, including a public post-commit durability
warning.

The product Surface keeps only a short-lived ready-preparation convenience
cache for the guided wizard. The durable preparation and command ledger remain
authoritative, so embedded and HTTP clients use the same request, revision,
idempotency, authentication-challenge, and recovery semantics. The retired
request-local discovery cache, hidden context-selected authentication path,
and Session-scoped connect routes are not product authorities.

Terminal login execution is an explicit caller capability, not wire data. A
bound embedded presentation may attach a local terminal runner to the
`prepare-auth` request context. HTTP carries the same method ID and receipt
contract but cannot carry that executable callback; selecting a terminal
method over HTTP therefore fails with `failed_precondition` before any login,
process restart, or child preparation effect. The principal-owned
`needs_auth` preparation remains unchanged and can be continued by a capable
embedded presentation with a new operation ID. Agent-managed authentication
methods work through both transports without this local capability.

## Authentication Recovery

Caelis preserves structured JSON-RPC call errors so the ACP
`auth_required` code (`-32000`) is detected by code rather than formatted
message text. Locally synthesized connection failures remain transport errors
and never acquire a peer JSON-RPC code. Authentication methods come only from
the Agent's `initialize.authMethods` response; persisted terminal identity is
used only to direct a non-interactive runtime back through `/connect`.

The bridge owns one authenticated-operation recovery path:

1. call the ACP operation;
2. if the response is not `auth_required`, return it unchanged;
3. select an exact declared method ID, reusing the persisted endpoint
   selection when available;
4. execute the method according to its declared type;
5. retry the original operation once.

This path covers Session open and resume as well as prompts and negotiated
steering on active controller, participant, and spawned-Agent Sessions.

Stable agent-managed methods call `authenticate` with the declared `methodId`
on the current ACP connection. A missing method type normalizes to `agent`, as
defined by stable ACP v1.

Preview terminal methods are out of band. During `/connect`, the client
advertises `clientCapabilities.auth.terminal`. Caelis closes the discovery
Agent process, launches the same configured executable with its base arguments
and environment plus only the method-declared argument and environment
additions, waits for a successful exit, then starts a new Agent process,
initializes it, and retries the Session request. A terminal method is never
sent to `authenticate`.

Controller and spawned-Agent runtime connections deliberately do not advertise
terminal authentication. They may recover a persisted agent-managed method
in-band. If a persisted terminal login expires, runtime returns an actionable
error directing the user through `/connect`, where the initiating Surface can
own the interactive process. This guidance uses the persisted terminal method
even when the runtime `initialize` response omits that method because terminal
capability was not advertised.

Recovery is single-shot. A second `auth_required` after successful
authentication is returned rather than looping or repeating an external
side effect. If authentication succeeds but `session/resume` then fails for a
non-authentication reason, the controller treats that as an unavailable stale
Session and falls back to `session/new`; method selection, login, and repeated
`auth_required` failures remain hard recovery errors.

See the upstream
[ACP authentication methods RFD](https://agentclientprotocol.com/rfds/auth-methods)
for the wire contract.

## Agent Input and Steering

Caelis uses the standard ACP input methods for external Agents. An idle child
receives `session/prompt` on its existing ACP Session. A running child receives
`_session/steering` only when the Agent advertised steering support during
`initialize`; otherwise active input is rejected explicitly. Caelis does not
advertise, call, or accept a private Agent-message method.

`SendMessage {to,message}` is a model-facing address adapter over those standard
input methods. Caelis classifies the operation as Agent communication before it
crosses the endpoint boundary and prefixes the standard ACP prompt with the
trusted sender identity. It has no delivery MessageID, durable mailbox,
target-lifecycle acknowledgement, or Task mutation. Each invocation dispatches
one input. The target Agent chooses when active steering is consumed, while the
existing child endpoint owner serializes transport operations and isolates
ambiguous outcomes. A transport loss or malformed post-dispatch response is
therefore reported as unknown and is not blindly retried.

Task observes child output, not the input operation. Input admission leaves the
Task unchanged. If the child later emits output or a terminal result, the
activity observer derives the next Task generation from those events and keeps
the same absolute stream cursor across endpoint resume or Runtime rehydration.
ACP Assistant, reasoning, tool, plan, and terminal notifications remain
first-class child events; their output `messageId` correlation is unrelated to
SendMessage input identity.

A Caelis-hosted child can address its parent or sibling through a trusted
Host-bound input sender. The Host derives the source from the exact durable
participant binding, routes an active parent through an explicit Agent-
communication submission, starts a parent Turn with that same semantic kind
when idle, and routes siblings through the same child input capability. The
parent Session persists `EventTypeContext` plus the trusted Actor and projects a
dedicated non-User transcript event. Standard ACP has no reverse
Agent-to-client routing method, so a third-party ACP host that does not supply
such a topology-aware sender cannot use `to=parent`; Caelis fails explicitly
rather than reviving a private wire extension.

After a Host or Session Runtime restart, the durable parent participant binding
still owns the child handle, placement, ACP Session ID, and Task identity. A
later idle input may recreate the endpoint process, resume that exact ACP
Session, and issue `session/prompt`; it never substitutes `session/new` or uses
Task state as input authority. Built-in managed child Sessions remain hidden
from ordinary lifecycle clients, and resume still requires the exact parent and
delegation binding.

`SendMessage` is the incremental channel for updates and questions. A child's
terminal answer remains its final response and is retrieved by the parent with
`Task read` or `Task wait`; sending the same terminal answer through both paths
creates duplicate narrative. The tool result confirms only that one ordinary
input dispatch returned successfully. It carries no target state or Turn
identity; Task read/stream derives those facts later from child output.

When an external participant itself invokes Spawn, that nested child remains
behind the participant process boundary. Caelis consumes its live child stream
locally and exposes at most one terminal `tool_call_update` per child Turn. The
FinalResponse is standard ACP tool result `content` on the participant's Spawn
call. That call is not mounted as a client terminal; no nested child narrative,
Task workspace, subagent roster entry, or subagent output overlay crosses into
the parent TUI. Live and replayed durable
participant results use the same ordinary tool panel and never infer a child
workspace from the Spawn name. This does not change the product-owned Main
Spawn workspace and Task-stream contract.

## Disconnect Safety

Disconnect is a Host-scoped AppServer command with an idempotency key and an
expected Host configuration revision. It removes the external Agent, its ACP
profiles, and eligible bindings in one configuration compare-and-save. A
committed configuration write is never undone with an unconditional rollback;
later assembly refresh failures are reported as committed warnings.

Disconnect changes the Host configuration available to future Session Runtime
activations. It does not scan durable Sessions, rewrite their controller or
participant history, or invalidate a Runtime that was already assembled. Such
an activated Runtime may continue to use the external Agent endpoint captured
in its immutable configuration snapshot until the Runtime is released, the
user switches away and later reactivates the Session, or the Host restarts.
After that boundary, a new activation reads the current configuration and the
disconnected Agent is unavailable.

No assembly lease or fresh Host admission check exists on participant Spawn,
attachment, or controller handoff. Those operations resolve from the active
Session Runtime snapshot. Configuration writers coordinate only for the short
atomic configuration compare-and-save; revision conflicts are reported to the
writer and do not block an already active Runtime.

## Agent Default Model

ACP model catalogs are optional. When a Session advertises no models, Caelis
creates one product-only `Agent default` profile so the connection remains
selectable. Its synthetic profile ID is never sent over ACP: runtime leaves the
Session model selection empty and preserves the Agent's own default.

Guided TUI onboarding selects the exact remote model but does not preselect a
reasoning effort. The resulting ModelProfile records the Agent-advertised effort
choices and current value as its capability and profile default. Fixed-handle
bindings and participant attachment still choose one explicit canonical effort;
the later selection, not onboarding, determines the effort sent for that work.

## Registry Snapshot

The npx-compatible catalog is generated from:

`https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json`

Control external-Agent onboarding owns the checked-in snapshot and its product
overlay:

- `catalog_registry_generated.go` contains generated upstream identity,
  description, version, package, arguments, and environment values;
- `catalog.go` owns Caelis IDs, display priority, preferred launcher, verified
  managed binaries, installed-command entries, and the custom-command entry;
- binary and uvx distributions remain excluded until Caelis owns verified,
  cross-platform installers for them.

Refresh from the repository root with:

```sh
cd app/gatewayapp/internal/agentregistry
go generate
```

Review the generated diff for version changes, package/argument/environment
changes, stable product-ID mappings, and unexpected additions or removals.
Then run the registry, launcher, `/connect`, architecture, and full quality
gates. Do not hand-edit the generated file.
