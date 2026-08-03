# External ACP Agents

This document owns the maintained product contract for local external ACP
Agent onboarding, authentication recovery, default model behavior, and the
checked-in ACP Registry snapshot. Layer ownership remains defined by
[Caelis Architecture](architecture.md).

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
`from`, and `message`. Caelis derives the authoritative source from the trusted
Spawn/Session binding rather than the wire `from` value. Delivery to a running
child is mid-turn; delivery to a completed child queues its next Turn on the
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
completed Task result.

`SendMessage` is the incremental channel for updates and questions. A child's
terminal answer remains its final response and is retrieved by the parent with
`Task read` or `Task wait`; sending the same terminal answer through both paths
creates duplicate narrative. Result fields are observation, not a second Task
lifecycle: `pending` means canonical context is durable for a later Turn,
`delivered` means a live target accepted submission, `running` means a new
message-authored child activity started, and `completed` is returned only after
that ACP activity closes. `startedTurn` marks that transition and `turnId`
groups the activity; neither is a completion guarantee.

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
