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
Agent messages on active controller, participant, and spawned-Agent Sessions.

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

## Agent Messages

Built-in Caelis ACP children support bidirectional Agent messages through the
negotiated `_caelis.dev/session/message` extension. An Agent advertises support
under `initialize.agentCapabilities._meta`; a client with an inbound handler
advertises the same key in `clientCapabilities`. Missing capability fails
explicitly and is never emulated with a new `session/prompt` or `Task write`.

The extension carries `sessionId`, `messageId`, `to`, optional display-only
`from`, and `message`. Caelis derives the authoritative source at the
principal-bound AppServer capability from an exact participant/controller
binding or the durable parent-Task/participant relation of a managed child,
rather than the wire `from` value. An unbound principal fails closed. An
untrusted `from` survives only as `display_from` event metadata and
never changes canonical `Event.Actor` or rebuilt model context. The target
Session revision and, for a managed child, the parent Session revision used to
resolve that relation are checked atomically with the canonical append. A
concurrent close, controller handoff, or participant detach therefore wins the
race and forces source resolution to run again; it cannot leave a message
written under a stale binding. Delivery to a
running child is mid-turn; delivery to a completed child queues its next Turn on the
same ACP Session. Child-to-parent and child-to-sibling delivery returns once
the routing boundary owns asynchronous delivery, independently of target
consumption or completion. Parent-side outbound audit is written only after
that ownership transfer and remains a mirror; the remote target Session owns
canonical message context. If that acceptance succeeds but the sender cannot refresh its local Task index, Caelis returns
`accepted_unpersisted` without a delivery error so callers do not blindly repeat
the queued effect with a new message ID.

For a message-authored Turn on a previously completed child, the spawning
runner owns the asynchronous ACP request after `SendMessage` returns. The ACP
child keeps `_caelis.dev/session/message` open until that Turn is terminal and
returns `state: completed`; the runner publishes that later outcome through the
Task lifecycle. The response also carries `startedTurn` and `turnId` for Task
observation. A non-terminal acknowledgement such as `state: running` does not
prove completion; the runner records `unknown_outcome` rather than a false
completed Task result. A request or observer disconnect detaches only that
observation and never cancels an accepted target Turn. Feed closure proves
`completed` only when the target terminal was observed without a feed or
reconnect error; otherwise the delivery outcome remains unknown.

After the parent host or Session Runtime restarts, the durable Task still owns
the child handle, placement, ACP Session ID, and Task ID. The first message to a
completed child lazily recreates only the endpoint process, resumes that exact
ACP Session, and then starts the next message-authored Turn on the existing
Task. It never substitutes `session/new` or a new handle. Built-in managed child
Sessions remain hidden from ordinary lifecycle clients: the internal resume
claim must match both the durable parent Session and Spawn Task recorded on the
child before that bridge instance may address it.

`SendMessage` is the incremental channel for updates and questions. A child's
terminal answer remains its final response and is retrieved by the parent with
`Task read` or `Task wait`; sending the same terminal answer through both paths
creates duplicate narrative. Result fields are observation, not a second Task
lifecycle: `pending` means canonical context is durable for a later Turn,
`delivered` means a live target accepted submission, `running` means a new
message-authored child activity started, and `completed` is returned only after
that ACP activity closes. `startedTurn` marks that transition and `turnId`
groups the activity; neither is a completion guarantee.

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
