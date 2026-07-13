# Control Client Protocol v1 — M2 Design

Status: release baseline complete; accepted 2026-07-13 after pure-read
`SnapshotState` semantics and lossless generated Go raw ACP round-trips passed
the full race, commit, regression, and release-dry-run gates.

Milestone: M2 — durable replay, reconnect, request-scoped Control commands,
and a thin HTTP/SSE app-server.

This document is the normative product-client contract and release boundary
accepted for M2. Layer and semantic ownership remain governed by
[Architecture](architecture.md), [Agent SDK Boundary](agent-sdk-boundary.md),
and [ACP Projection Architecture](acp-projection-architecture.md).

## Outcome

M2 gives every Caelis presentation client the same Control-owned session feed
and request-scoped command contract:

```text
TUI / headless / ACP bridge ----+
                                +-> ports/controlclient -> Control
HTTP JSON + SSE app-server -----+          |
                                           +-> Session Feed Broker
                                                |- durable session.Event lane
                                                `- transient live lane
```

The protocol has three independent compatibility dimensions:

| Dimension | M2 value | Owner |
| --- | --- | --- |
| ACP semantics/wire | negotiated ACP v1 | `agent-sdk/session` semantics and `protocol/acp/*` wire |
| Caelis Envelope/feed | `caelis.control.envelope/v1` | `protocol/acp/eventstream` |
| HTTP API | `/api/control/v1` and OpenAPI 3.1 | `surfaces/appserver` plus checked-in OpenAPI |

`agent-sdk/session.Event` remains durable semantic truth. An
`eventstream.Envelope` is a versioned projection and delivery record, not a
second session store or model-context authority.

## Release Boundary

M2 closes the infrastructure baseline for the next release. All implemented
presentation clients now enter through one Control command plane and consume
one ordered Envelope feed; durable replay, transient-gap reporting, approval
reconnect, mutation fencing, command idempotency, HTTP/SSE transport, and wire
generation have production entry points and release-level regression gates.
The release does not require a Surface-specific stream, replay cache, approval
route, or ambient Session lookup.

Completion is scoped to capabilities that this version advertises. In
particular, client-hosted ACP terminal execution remains unsupported and is
reported as such; the Zed empty-terminal anchor remains a documented
compatibility projection. The strict ACP corpus and T0 terminal tradeoff are a
separate compatibility track. Runtime process-restart continuation, durable
transient bytes, raw ACP HTTP/WebSocket, GUI, Goal lifecycle, and the Agent
Manage Loop remain explicit non-goals below and must ship in later versions.

M2 should be reopened only for a correctness, security, durability, ordering,
lease-fencing, or wire-compatibility regression in this accepted baseline.
New product features require a later-version design rather than widening this
release candidate.

## Protocol Contract

### Delivery modes

`eventstream.Delivery` has one required typed mode:

- `canonical`: durable canonical Session semantics. Model context includes the
  event only when the SDK replay rules classify its payload as model-visible.
- `mirror`: durable client semantics excluded from parent model context. M2
  uses this for linked child ACP history and reconnectable approval prompts.
- `transient`: process-local live delivery with no process-restart guarantee.

No Boolean combination may represent delivery. `mirror` means a durable
client-facing semantic event; it never means a flattened parent-tool text copy.

### Durable child origin and relation

A durable child mirror records a typed reusable origin on `session.Event`:

- parent Session ID;
- child scope (`subagent` or `participant`);
- stable scope/task/delegation ID;
- participant or child ACP Session ID when present;
- child source event identity;
- the actual parent Spawn/Task tool call and tool name;
- owning turn/controller origin already represented by `EventScope`.

The origin is storage truth and is projected into Envelope `scope`, `scope_id`,
`participant_id`, and `parent_tool`. `_meta` is never read to restore these
fields. Stable child append idempotency binds the parent Session, child scope,
scope ID, child ACP Session, child source identity, and projection-relevant
payload. Repeating the same identity and payload deduplicates; a changed payload
under the same identity returns `session.ErrEventConflict`.

Child input follows this order:

```text
child Frame.Event
  -> normalize semantic session.Event
  -> assign typed child origin and stable source identity
  -> append VisibilityMirror to the parent Session
  -> project the stored event (including Seq)
  -> publish through the Session Feed Broker
```

The parent receives only one canonical Spawn/Task final result. Child message,
thought, tool, diff, location, plan, permission, and lifecycle mirrors never
enter parent model context.

### Projection identity and feed position

`EventID` remains the source session event ID. `ProjectionID` remains the
stable semantic identity of one projected Envelope when one source event
expands into multiple Envelopes. Clients do not submit either value as a resume
token.

Every published Envelope carries a typed feed position:

- durable position: Session event `seq` plus zero-based projection index;
- transient position: the latest durable `(seq, projection index)` anchor plus
  broker generation and monotonic transient sequence.

Ordering compares durable positions first. Transient positions are ordered only
within the named broker generation and durable anchor. They are not durable
storage offsets.

### Cursor: the only public resume token

`Envelope.Cursor` is the only public resume token. It is an opaque,
base64url-encoded versioned payload plus HMAC-SHA-256 signature. The payload
binds:

- cursor codec version;
- Session ID;
- complete typed feed position;
- issuance metadata needed for rotation diagnostics, but no authorization
  principal.

The signing secret is persistent server configuration, is never returned to a
client, and must survive process restart for durable fallback. Decoding rejects
unknown versions, malformed or forged signatures, a token for another Session,
and impossible positions. Authorization is re-evaluated separately for every
request; a valid token grants no Session access.

### Replay filter

Client replay has an independent SDK filter that includes `canonical` and
`mirror`, and excludes `ui_only`, `overlay`, `notice`, and `journal`. Existing
model-context and invocation replay filters do not change. Session Readers
page forward by `Event.Seq` and never require loading all events to serve a
client backfill.

## Session Feed Broker

There is one broker per active or replayed Session and any number of
subscribers. It is owned by Control and starts when a Turn is registered, before
a Surface can subscribe. The existing Turn live-feed broker is narrowed to
Turn ingress and retains source `Read`, cursor commit, final-source barrier,
drain/ack, and unique main-terminal behavior.

The Session broker is the sole consumer of the Turn ingress feed. It publishes:

- durable lane: projections of stored `session.Event` values with canonical or
  mirror delivery;
- transient lane: exact RunCommand bytes, notices, and lifecycle that has no
  durable source.

TUI, headless, ACP bridge, and app-server receive independent subscriptions.
Closing a subscriber never cancels a Runtime Turn, Spawn, participant, or task.
A child terminal closes only its child scope. The Turn ingress final-read
barrier flushes already materialized frames and never waits for or cancels an
asynchronous Spawn.

### Bounds and slow consumers

Broker configuration has independent positive bounds for ring event count,
ring encoded bytes, and event TTL. Every subscriber has a separate bounded
queue. Publication never blocks Control on subscriber I/O. A subscriber whose
queue fills is closed with a typed slow-consumer reason and resumes from its
last delivered Cursor.

The ring keeps cloned whole Envelopes. Byte accounting uses the encoded
Envelope size used by the wire codec. Eviction preserves order and may remove
transient or durable events; durable events remain recoverable from the Session
Reader.

### Atomic subscribe

Subscribe is one broker transaction:

1. validate authorization and Cursor;
2. register the subscriber paused;
3. capture the broker high-water position;
4. collect eligible ring and/or paged durable backfill through that high-water;
5. deduplicate durable projections by monotonic durable position and transient
   ring entries by their signed position token;
6. splice queued live events strictly after the high-water;
7. unpause delivery.

Transient Publish may continue during durable I/O; durable Publish shares the
same sequencer as Reader scanning and cannot advance beyond an unfilled durable
gap. Live events queue behind the paused subscriber boundary. The subscriber
receives each semantic Envelope once.
`FeedSubscription.BackfillDone` closes after the captured prefix has been
delivered and before queued live splice delivery, so finite `/events` readers
do not wait for an Envelope when a fallback prefix is empty.

Subscribe returns one of:

- `exact`: the requested ring position and all following transient/durable
  events are available;
- `durable_fallback`: the transient position was evicted or belongs to a prior
  process generation. Backfill begins after the token's last durable anchor and
  sets `transient_gap=true`.

In the same process while the ring retains the cursor, RunCommand bytes,
ANSI sequences, UTF-8 fragments, cursor order, and exit state are exact and
nonduplicated. After process restart only canonical and mirror delivery is
guaranteed.

## SessionState and Approval Reconnect

Bootstrap returns one typed `SessionState` containing:

- protocol, Envelope, and HTTP API versions;
- Session ID and revision;
- workspace key, CWD, title, and display metadata;
- replay boundary Cursor, resume mode, and transient-gap flag;
- run, handle, and turn state;
- controller binding and epoch;
- durable participants;
- active approval head details and queued count;
- negotiated client capabilities;
- `goal_bootstrap_supported=false`;
- `manage_loop_bootstrap_supported=false`.

Capabilities include `client_managed_terminal=false` and
`caelis_terminal_stream=true`. Bootstrap is consistent with one Session
revision and one broker replay boundary. Control retries when either changes
during assembly; after the bounded retry budget it returns a typed revision
conflict rather than a mixed snapshot.

Before reading a boundary, Control incrementally scans the Session Reader from
the last consumed durable Seq and publishes every newly discovered projection
to existing subscribers. The same sequencer gap-fills a durable Publish before
advancing high-water. This seeds a new process and closes the
append-before-publish interval without reloading the full event log.

Session ID is the only resource identity. Workspace key and CWD are optional
list filters, display metadata, and authorization inputs; they never select a
current Session or repair a missing Session ID.

All main, Side ACP, child, Guardian, and auto-review approvals share the
existing Control FIFO. Before the active head is published, Control appends a
`VisibilityMirror` permission event with typed approval request ID, scope,
scope ID, parent tool, and the unmodified normalized ACP permission payload.
Only the active head is exposed by bootstrap and may be resolved; bootstrap
also reports `queued_count` without queued request details.

A live waiter remains owned by the Session coordinator after client disconnect.
Turn ownership supplies origin and cancellation, while a detached child may
outlive its parent Turn without leaving the Session FIFO. Resolving the same
active ID through a new subscription succeeds. Unknown, stale, duplicate, and
queued IDs return conflict. Session close, owner abandonment, and startup
recovery sweep settle abandoned durable approval mirrors. M2 does not restore a
Runtime continuation after process restart: a persisted active approval with no
matching live waiter becomes interrupted/cancelled and is never exposed as an
actionable request.

Durable Session state uses the same execution fence as canonical events.
`StateWriter` accepts only request-scoped replace/update values carrying a
Session reference, expected revision, and `MutationGuard`; the legacy
unfenced signatures do not exist. File and memory stores validate revision and
active lease authority before invoking the state callback or committing a
replacement. A matching Runtime fence and explicitly overlap-safe Control
purposes may write during an active lease; unscoped, stale Runtime, stale
Control, and stale-revision requests fail without changing state.
`SnapshotState` is a pure read: a compatibility document with no `state` field
is returned as an empty map without a write, revision change, or timestamp
change. Any persistent document repair must use an explicit fenced mutation.

## Request-Scoped Control Commands

`ports/controlclient` is transport-neutral and deliberately narrower than
`protocol/acp/control.Service`. It owns principals, command requests/outcomes,
SessionState, subscriptions, and capabilities; it does not own persistence,
ACP wire DTOs, HTTP, or runtime assembly.

Every write contains:

- `operation_id` (HTTP: `Idempotency-Key`);
- trusted `Principal` injected by the adapter context, never decoded from the
  request body;
- Session ID;
- action-specific target identity;
- expected Session revision and/or expected controller epoch where applicable;
- explicit handle/run/turn target for live Turn operations.

Typed outcomes are `accepted`, `committed`, `conflicted`, `rejected`, and
`unknown`. An error may carry transport status, but clients decide recovery
from the typed outcome.

Supported v1 operations are:

- Session create, list, inspect, and close;
- prompt, steer, and cancel;
- resolve active approval;
- participant attach, prompt, cancel, and detach;
- controller handoff.

Session close first cancels the exact active Turn target, waits for Runtime
producer quiescence, then atomically commits a durable `closed` lifecycle and a
state gate that rejects later writes. Participant cancel is accepted only when
handle/run/turn, active kind, and participant ID all match in Gateway Control.

### Authorization

`internal/controlclient.Authorizer` authorizes the trusted principal against
the explicit Session ID and action. Workspace/CWD may constrain policy but may
not resolve identity. Cross-principal access and a body-supplied principal are
rejected. HTTP bearer/token credentials are never accepted from a query
parameter.

### Durable operation ledger and failure guarantee

`OperationStore` keys records by `(principal, operation_id)` and binds action,
Session ID, target identity, and canonical request digest. The operation intent
is durable before dispatch. The same key and request returns its recorded typed
result; the same key with a changed action, target, or digest conflicts.

The operation ID is also passed to downstream transaction/event/task
idempotency identities. This closes the common window where a durable effect
commits but the HTTP response is lost.

Caelis does not claim general exactly-once external effects. If Control cannot
prove from the operation ledger or downstream reconciliation identity whether
an effect committed, the recorded and returned outcome is `unknown`. It must
not retry the effect unconditionally. This guarantee applies to every write,
including handoff and participant operations.

## HTTP JSON and SSE API

The checked-in OpenAPI 3.1 document is the HTTP wire truth. Generation uses a
pinned tool version and produces checked-in standalone Go DTOs and TypeScript
types. A production conformance gate marshals every `ports/controlclient`
request/response, every Envelope kind, every standard ACP update, and a raw ACP
extension through the real Go wire types and validates the JSON against the
OpenAPI component schemas. A second gate decodes and re-encodes a complete raw
extension Envelope through the generated Go DTOs and requires JSON equivalence.
`make client-protocol-generate` refreshes generated files;
`make client-protocol-check` proves a clean regeneration and is part of
`make commit-check`.

All resources are under `/api/control/v1`:

| Method | Path | Semantics |
| --- | --- | --- |
| `GET` | `/sessions` | list authorized Sessions; workspace is an optional filter |
| `POST` | `/sessions` | create Session |
| `GET` | `/sessions/{session_id}/state` | consistent SessionState bootstrap |
| `DELETE` | `/sessions/{session_id}` | close Session/control resources |
| `GET` | `/sessions/{session_id}/events` | finite backfill after Cursor |
| `GET` | `/sessions/{session_id}/stream` | SSE subscription after Cursor |
| `POST` | `/sessions/{session_id}/prompt` | start an explicit prompt Turn |
| `POST` | `/sessions/{session_id}/steer` | submit to explicit live Turn target |
| `POST` | `/sessions/{session_id}/cancel` | cancel explicit live Turn target |
| `POST` | `/sessions/{session_id}/approvals/{approval_request_id}/resolve` | resolve active FIFO head |
| `POST` | `/sessions/{session_id}/participants` | attach participant |
| `POST` | `/sessions/{session_id}/participants/{participant_id}/prompt` | prompt participant |
| `POST` | `/sessions/{session_id}/participants/{participant_id}/cancel` | cancel participant Turn |
| `DELETE` | `/sessions/{session_id}/participants/{participant_id}` | detach participant |
| `POST` | `/sessions/{session_id}/handoff` | authorize/commit controller handoff |

Every write requires `Idempotency-Key`. Revision CAS maps from `If-Match` and
the generated DTO's expected revision; contradictory values are rejected.
Controller epoch and live Turn target remain explicit request fields.
Mutation responses declare the handler's complete `200`, `202`, `400`, `401`,
and `409` status set. Read endpoints declare their authentication,
authorization, and malformed-request responses; SSE additionally declares its
streaming-unavailable response.

Standard ACP update variants remain closed and discriminated. Unknown
non-empty `sessionUpdate` values use the explicit `ACPRawUpdate` extension
variant, which preserves vendor fields, is represented by the generated
TypeScript union, and excludes every known standard discriminator so `oneOf`
remains unambiguous. Generated Go represents the open raw object as a JSON-value
map and the containing union as `json.RawMessage`; both standalone raw updates
and complete Envelopes therefore preserve unknown vendor properties.

SSE rules:

- the first named `caelis.control.resume` event and matching response headers
  carry resume mode, transient-gap, and boundary Cursor;
- `id` is exactly `Envelope.Cursor`;
- `data` is one complete JSON Envelope;
- `Last-Event-ID` and `after` are accepted as Cursor inputs, but a mismatch is
  a bad request;
- heartbeats are SSE comments only and never Envelopes or resumable events;
- a slow-consumer close is visible as stream termination; recovery uses the
  last delivered `id`.

The `caelis serve` product entry point defaults to a loopback listener. Starting
on a non-loopback address without configured authentication fails closed; a
static Bearer token is available for the product server. `surfaces/appserver`
only extracts
credentials into a trusted principal, maps DTOs and errors, and streams
Envelopes. It contains no Runtime, approval policy, broker, session store,
participant/handoff policy, or persistence logic.

## Package Ownership

| Responsibility | Owner |
| --- | --- |
| canonical events, child typed origin, paged Session Reader | `agent-sdk/session` |
| Envelope, delivery mode, feed position, cursor codec contract | `protocol/acp/eventstream` |
| ACP semantic projection | `protocol/acp/projector` |
| transport-neutral commands and SessionState | `ports/controlclient` |
| feed broker, durable child recorder, operation store, authorizer, command service | `internal/controlclient` |
| shared Turn/task ingress and final-read barrier | `internal/controlclient/turningress`; every in-process and network adapter consumes the same merged feed |
| HTTP/SSE DTO mapping and authentication extraction | `surfaces/appserver` |
| dependency assembly, listener configuration, persistent secret path | `app/*` and `cmd/caelis` |

`surfaces/appserver` may import `ports/controlclient` and
`protocol/acp/eventstream`; it must not import `app/*`, repository
`internal/*`, `agent-sdk/runtime`, policy, task stream, or session persistence
implementations. `agent-sdk/*` must not import `protocol/*`, `ports/*`,
`surfaces/*`, `app/*`, or repository `internal/*`.

## Migration

M2 is a pre-v1 contract cleanup:

- replace `delivery.transient` with `delivery.mode`;
- keep `EventID` and `ProjectionID`, but stop accepting either as a client
  resume token;
- replace replay-then-live Surface paths with `controlclient.Subscribe`;
- remove gateway task-panel replay augmentation and every child-history
  reconstruction from TUI text or `_meta.terminal_output`;
- keep the Zed empty terminal anchor and `_meta.terminal_output` compatibility
  route while declaring the Caelis client terminal capability separately;
- remove ambient current-session/CWD fallback from the client-protocol adapter;
- keep `protocol/acp/control.Service` for current ACP/TUI transitional commands
  without adding M2 request-scoped business operations to it.

No compatibility waiver may hide an unintended SDK dependency or a second
resume-token contract.

## Non-Goals

- Runtime process-restart continuation or recreation of an approval waiter;
- durable replay of terminal bytes or other transient lane data;
- ACP `terminal/create` execution ownership changes;
- raw remote ACP over HTTP or WebSocket;
- GUI implementation;
- Goal lifecycle or Agent Manage Loop behavior (only unsupported capability
  fields are reserved);
- deterministic workflow graph, node/edge DSL, or scheduler;
- autonomous Agent handoff authorization;
- a persisted UI transcript cache.

## Acceptance Matrix

The milestone is complete only when every row passes with whole-object/event
comparisons where applicable.

| Gate | Required evidence |
| --- | --- |
| M2-A protocol | typed delivery modes; typed durable child origin; stable idempotency conflict; paged Reader; canonical+mirror client filter; no synthetic task-panel replay; child semantic fixtures; parent model-context round trip unchanged |
| M2-B broker | ordered durable sequencer and gap fill; live publication of scanned commits; atomic subscribe under concurrent transient publish; exact and durable fallback modes; bounded ring/TTL/bytes without forgetting durable dedupe; slow-subscriber disconnect; forged/version/cross-Session cursor rejection; exact RunCommand bytes/ANSI/UTF-8/exit reconnect; child terminal and final-read barrier behavior |
| M2-C state/approval | revision-consistent SessionState; request-scoped StateWriter revision/lease fencing in file and memory stores; pure-read SnapshotState for missing compatibility state; global Session identity; Session-scoped FIFO across Turn owners; detached child after parent terminal; active mirror-before-publish under Runtime lease; reconnect resolution; stale/duplicate/queued conflicts; cancel/close/terminal/startup sweep |
| M2-D commands | all named commands are request-scoped; trusted-principal authorization; operation intent before effect; duplicate and changed-payload behavior; revision/epoch/turn conflicts; semantic close gate; exact participant-kind/identity cancel; explicit unknown-outcome coverage for every write |
| M2-E app-server | strict checked-in OpenAPI 3.1 wire schemas with actual response statuses and explicit raw ACP extensions; schema-driven standalone Go and complete TypeScript outputs; all-request/response/Envelope JSON Schema conformance plus generated-Go raw Envelope round-trip equivalence; HTTP golden tests; SSE resume boundary metadata, Cursor/data parity, and heartbeat comments; production `caelis serve`; loopback/auth fail-closed configuration; thin-surface dependency test |
| M2-F integration | built-in/external normalized parity; restart rebuild of transcript/child tools/participants/controller; cross-workspace global ID; in-process/TUI/headless/ACP bridge/HTTP whole-Envelope equality; generated clean-tree check; architecture gates |

Production-shaped scenarios additionally cover sibling children with identical
text and terminal IDs, overlapping deltas, one child terminal plus one parent
result, main/Side/child approval ordering, ring eviction, exact terminal
compatibility paths, and two workspaces sharing one global Session namespace.
Lease-shaped coverage includes startup longer than TTL, cancellable file-lock
waits, early consumer close before producer completion, detached child fence
clearing, and approval/child Control writes while a Runtime lease is active.
