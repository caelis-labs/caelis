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
                                +-> ports/controlclient (transitional) -> Control
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
subscribers. It is owned by Control. The first-party adapter registers a
`SubscribeFromNow` boundary before `BeginTurn`, then attaches Turn ingress only
after the Surface claims `Turn.Events`; there is no interval in which a live
Turn can overflow a subscription that the caller is not yet able to consume.
The existing Turn live-feed broker is narrowed to
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
ring encoded bytes, event TTL, and subscriber queue size. Public resumable
`Subscribe` consumers have independent bounded queues; a slow consumer is
closed with a typed reason and resumes from its last delivered Cursor.

`SubscribeFromNow` is the internal active-Turn handoff, not a remote replay
subscription. It registers the no-history boundary before Turn start, binds the
subscription lifetime to the caller context, and uses the same independent
bounded slow-consumer disconnect as other subscriptions. Session publication
never waits on subscriber I/O: an unclaimed or paused Surface therefore cannot
stall a sibling ingress, durable sequencing, or terminal delivery. The adapter
attaches the new Turn's own ingress lazily after the Surface claims the event
stream, so a fast Turn cannot overflow an unconsumable pre-return subscription.
If unrelated same-Session traffic exhausts the queue before handoff, the
Surface receives the typed slow-consumer failure and can resume from its Cursor;
the broker never silently truncates the feed or freezes the Session.
After handoff, `AttachTo` fences the prepared target while the owning Turn event
and any durable storage gap enter the global Session sequence. All Envelopes
accepted during that fence are collected for the target in actual acceptance
order under hard event and encoded-byte bounds. The broker releases its lock
and durable sequencer before delivering the bounded target batch, so a slow
first-party Surface can wait only on its own ingress delivery and only until the
configured subscriber stall timeout. Expiry disconnects that target with the
typed slow-consumer reason. Subscription context or Turn cancellation cancels
a blocking target read/wait and detaches that delivery, while the same
attachment continues untargeted Session publication for sibling subscribers.
Broker or Turn close releases the complete attachment. Unrelated Session events
retain non-blocking bounded fan-out and may trigger the target's typed
slow-consumer failure immediately when its queue is full.

Turn cancellation and delivery failure remain producer-barriered. The adapter
records the typed requested outcome, closes the prepared subscription, and
idempotently cancels the owning Runtime handle. `AttachTo` removes only target
delivery and continues publishing that same ingress into the shared Session
feed until handle `ACPEvents` closes after Runtime producer and lease
completion. The Turn ingress broker records its single authoritative final
error/terminal; the local wrapper and sibling TUI, SSE, or GUI subscribers use
that same state and identity. Source-delivery failure likewise cancels once and
waits for producer close before the final-source barrier and unique `failed`
terminal. `Close` remains an explicit delivery teardown, emits no synthetic
terminal, and is not a substitute for these barriers.

The ring keeps cloned whole Envelopes. Byte accounting uses the encoded
Envelope size used by the wire codec. Eviction preserves order and may remove
transient or durable events; durable events remain recoverable from the Session
Reader.

### Atomic subscribe

Subscribe is one broker transaction:

1. validate authorization and Cursor;
2. enter the feed acceptance sequencer and obtain an atomic SDK event
   checkpoint containing the durable Session,
   source-sequence high-water, and last client-replay event;
3. install that high-water directly for a cold broker, or publish only a warm
   broker's missing durable suffix, then capture its acceptance watermark and
   bounded ring before leaving the sequencer;
4. preflight one bounded durable page, then return the typed feed cut;
5. stream durable pages and the captured ring in acceptance order through the
   fixed checkpoint without materializing the complete history;
6. verify that the shared ring still contains every acceptance after the
   watermark, install ordinary live fanout under the broker lock, and deliver
   that suffix exactly once;
7. if the suffix was overtaken, close with typed `FeedGapError` containing a
   signed retry Cursor and `transient_gap=true`.

Durable Publish shares the short checkpoint sequencer. Transient Publish may
interleave while a warm broker catches up and remains ordered by its signed
anchor in the captured ring. All Publish continues during the longer paged
backfill phase. Prepared
reconnects do not enter an ordinary subscriber queue; the shared bounded ring
is their continuation buffer, so a slow history reader cannot self-disconnect
before `Subscribe` returns merely because the default live queue is small.
`FeedSubscription.Backfill` streams only the captured prefix and closes before
`FeedSubscription.Events` exposes the live splice. `BackfillDone` mirrors that
phase boundary. Finite readers collect the Backfill stream and then close the
subscription; there is no parallel materialized backfill slice.

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
`caelis_terminal_stream=true`. `Reconnect` returns `SessionState` and the
continuation from the same event checkpoint/feed cut. Its resume mode,
transient-gap flag, boundary position, and Cursor are copied from that cut.
Runtime/approval state is sampled only after the continuation is prepared, so
ordinary high-frequency Publish cannot starve bootstrap through repeated
whole-Session optimistic retries; later changes arrive on that continuation.

Before reading a warm-broker boundary, Control incrementally scans the Session
Reader from the last consumed durable Seq and publishes every newly discovered
projection to existing subscribers. A cold broker installs the checkpoint
high-water without replaying complete history into its ring. The same
sequencer gap-fills a durable Publish before advancing high-water. This seeds a
new process and closes the append-before-publish interval without reloading the
full event log.

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
recovery sweep conditionally settle abandoned durable approval mirrors. The
Store atomically rechecks the exact request event, Session revision, pending
index, and lease guard before appending the recovery settlement; a concurrent
real resolution therefore wins without a conflicting startup event. M2 does
not restore a
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

The file Store keeps a transaction WAL through event-log, document, and Session
index commit, deleting it only after the index upsert succeeds. Initial Session
creation uses the same zero-event WAL boundary, so an index failure cannot
orphan a valid document or allow the same Session ID to be recreated. Every
process performs one legacy WAL scan per root and a durable pending marker
requests later scans after a committed interruption. Unix replacement is
followed by parent-directory sync; Windows document/WAL replacement uses
`MOVEFILE_WRITE_THROUGH` and startup scanning does not rely on marker durability.

## Request-Scoped Control Commands

`ports/controlclient` is transport-neutral and deliberately narrower than
`protocol/acp/control.Service`. It owns principals, command requests/outcomes,
SessionState, subscriptions, and capabilities; it does not own persistence,
ACP wire DTOs, HTTP, or runtime assembly.

This describes the accepted M2 API and current import path, not the long-term
package destination. The path is frozen: new Control operations and domains
belong under `control/*`, while later bounded migrations preserve the accepted
wire and replay semantics.

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

After the backend returns, Control persists the now-known result with a short,
Control-owned context detached from client cancellation. A disconnect after an
effect commits cannot leave the ledger at intent-only `unknown`; a real ledger
write failure remains bounded and is still reported as `unknown`.

Caelis does not claim general exactly-once external effects. If Control cannot
prove from the operation ledger or downstream reconciliation identity whether
an effect committed, the recorded and returned outcome is `unknown`. It must
not retry the effect unconditionally. This guarantee applies to every write,
including handoff and participant operations.

### Operation idempotency retention

The durable operation ledger provides a minimum terminal-result retention
window, not an unbounded history. The production default is 30 days and is
named in code as `DefaultOperationTerminalRetention`; operators may override it
with `--control-operation-retention`, `CAELIS_CONTROL_OPERATION_RETENTION`, or
the Control host `Config`. For a fresh root, zero-valued host configuration
selects the default; for an existing root it adopts the persisted policy.
Operators must provide an explicit value to replace that policy, and
non-positive explicit values are rejected. The active window is persisted
atomically in the `control-operations` root so every process sharing that root
uses one policy. A deliberate policy change is installed during host assembly;
already-open stores fail closed until reopened instead of silently mixing
policies for new work and maintenance. An already-started operation may still
persist its known result using its record snapshot, so rolling configuration
cannot strand a committed effect as intent-only `unknown`. The parent passes
the effective value as an explicit flag to spawned self ACP processes, so an
inherited environment variable cannot replace the shared-root policy.

Each newly begun record snapshots the active window. When Control persists a
proven terminal result it also persists an absolute `retain_until` deadline,
measured from the monotonic terminal `UpdatedAt`. Policy changes therefore do
not shorten a window already promised to an existing operation. The root policy
separately persists a high-water retention for legacy records that lack a
snapshot; it is the maximum policy ever installed for that root and cannot
decrease. A legacy `committed` or `conflicted` record materializes that
high-water window on a safe read or sweep. A legacy `rejected` record is never
age-reclaimed because older Control versions used that outcome for unclassified
failures whose external effect may be unknown.

Before `retain_until`, the same `(principal, operation_id)` and the same bound
intent stably replay the old result; changing action, Session, target, or
canonical digest remains `ErrOperationConflict`. Expiry makes a record eligible
for maintenance but does not promise immediate deletion: until deletion, it
continues to replay or conflict. After the canonical record is durably removed,
the ID may be treated as a new operation with either the same or a different
payload and may execute its effect again. Clients must therefore generate
globally unique operation IDs and must not use window expiry as a safe retry or
reuse signal.

Automatic TTL applies only to proven terminal outcomes:

- new-schema `committed`, `conflicted`, and `rejected` transition from retained
  to eligible at `retain_until`; `rejected` specifically means Control proved
  that no effect committed;
- legacy `rejected` remains indeterminate because its old encoding did not
  prove that distinction;
- an unclassified backend error is `unknown`, not `rejected`;
- intent-only records (`Result == nil`) may be in flight or may represent a
  crash with an unproved external effect, so ordinary TTL never removes them;
- persisted `accepted` and `unknown` results also remain protected;
- malformed records, retention snapshots/deadlines that do not exactly agree,
  invalid outcomes or timestamps, key/path mismatches, symlinks, and unknown
  record versions remain in place fail closed.

There is currently no proof-based reconciliation transition for intent-only,
`accepted`, or `unknown` records. They remain conservative idempotency guards
and may still accumulate. A future reconciliation API may move them only to a
proved terminal outcome using the downstream transaction/event/task identity;
ordinary age is not proof.

Maintenance is Control persistence behavior in `internal/controlclient`, not an
SDK, Surface, or wire-protocol concern. `MemoryOperationStore` and
`FileOperationStore` expose the same bounded `Sweep` semantics and lightweight
result counts. `FileOperationStore` opportunistically starts a traversal from
`Begin` no more than once per hour per in-process root when no backlog exists;
there is no background goroutine. If a bounded batch reports more work, the
next `Begin` may continue the cursor immediately, allowing command traffic to
catch up instead of capping production cleanup at one batch per hour. One call
still inspects at most 256 directory entries, removes at most 128 files, and has
a 100 ms soft processing budget. Its lifecycle-managed directory cursor
advances across calls so protected or damaged entries cannot permanently starve
later terminal records. Old writer temp files use a separate 24-hour grace
period; young temps and unrelated files are ignored.

Sweep classification and removal hold the existing process-local root lock and
cross-process root file lock for the complete critical section, so `Sweep`
racing `Complete` sees either an indeterminate intent or a freshly completed
deadline and cannot lose the result. On Unix, successful unlink is followed by
parent-directory sync. On Windows, the canonical record is first moved with
`MOVEFILE_WRITE_THROUGH` to a non-canonical GC temp before best-effort removal;
a crash can leave only reclaimable temp residue. Cleanup errors are returned by
explicit `Sweep` but opportunistic maintenance never changes the outcome of
`Begin` or `Complete`.

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
`403`, `409`, and `500` status set. Read endpoints declare their malformed,
authentication, authorization, conflict, and internal-error responses; SSE
additionally declares its streaming-unavailable response.

### Lossless integer wire contract

JavaScript-facing integers that may exceed `Number.MAX_SAFE_INTEGER` use the
OpenAPI `Uint64Decimal` schema: a canonical, unsigned base-10 JSON string with
no sign or leading zeroes. This applies to `expected_revision`, command and
Session revisions, durable feed `seq`, transient anchor `seq`, transient
`sequence`, and controller/participant `context_sync_seq`. The corresponding
Go domain values remain `uint64`; `surfaces/appserver` maps them at the HTTP
DTO boundary. `If-Match` carries the same decimal value as one quoted ETag.

Known Caelis metadata counters use the same representation. The complete typed
set is runtime task `output_cursor`, `event_cursor`, and `turn_seq`; runtime
stream `truncated_before`; root `from`/`to.context_sync_seq`; Caelis and SDK
prompt/cache/completion/reasoning/total/context-window usage counters; SDK
`cost_micros` and `context_window_tokens`; and compact `revision`,
`contract_version`, `summarized_through_seq`, `source_event_count`,
`total_tokens`, and `context_window_tokens`. ACP `usage_update.size` and
`usage_update.used` are also decimal strings in the HTTP/SSE DTO even though
the reusable Go ACP type remains `int`. This is an intentional product-wire
projection, not a change to the reusable ACP/runtime domain type.

Durable `Event._meta` decoding retains numeric tokens as `json.Number`, and
compact checkpoints persist their integer metadata as canonical decimal
strings. Rebuild and projection therefore reach the HTTP mapper without a
`float64` precision loss. Other typed Event fields and open Tool/Protocol
payloads keep their existing decoder behavior.

Arbitrary JSON extensions may not emit a numeric token outside the inclusive
JavaScript-safe range
`[-9007199254740991, 9007199254740991]`; extension-defined larger integers
must be decimal strings. The server rejects an unsafe extension number instead
of sending JSON that a JavaScript parser would silently round.

Wire conformance covers `9007199254740991`, `9007199254740992`,
`9007199254740993`, and `18446744073709551615` through HTTP encode, generated
Go/TypeScript-facing DTO shape, and decode. The generated TypeScript alias is
`Uint64Decimal = string`; no uint64 wire field is `number` or
`number | string`.

Standard ACP update variants remain closed and discriminated. Unknown
non-empty `sessionUpdate` values use the explicit `ACPRawUpdate` extension
variant, which preserves vendor fields, is represented by the generated
TypeScript union, and excludes every known standard discriminator so `oneOf`
remains unambiguous. Generated Go represents `ACPRawUpdate` as
`json.RawMessage` and also keeps a containing union with an open variant raw;
both standalone raw updates and complete Envelopes therefore preserve unknown
vendor properties and numeric tokens without a `float64` intermediate.

SSE rules:

- the first named `caelis.control.resume` event and matching response headers
  carry resume mode, transient-gap, and boundary Cursor;
- `id` is exactly `Envelope.Cursor`;
- `data` is one complete JSON Envelope;
- `Last-Event-ID` and `after` are accepted as Cursor inputs, but a mismatch is
  a bad request;
- heartbeats are SSE comments only and never Envelopes or resumable events;
- a recoverable splice or slow-consumer gap emits a final named
  `caelis.control.resume` event with `durable_fallback`,
  `transient_gap=true`, and its signed retry Cursor before termination.

### HTTP trust and error contract

Every HTTP handler, including an explicitly assembled in-process test handler,
requires an authenticator and Host allowlist; there is no network form of an
implicit `LocalPrincipal` or trusted-mode switch that a TCP server can enable.
The `caelis serve` product entry point defaults to `127.0.0.1:7777` and, unless
`CAELIS_CONTROL_TOKEN` is set, creates or loads
`<store-dir>/control-http.token`. The file contains a random 256-bit bearer
credential. On Unix it must be an ordinary, current-user-owned non-symlink file
with exact mode `0600`; on Windows it must be current-user-owned with a
protected DACL limited to that user, LocalSystem, and Administrators. Creation
flushes a secured temporary file and publishes it through an atomic no-clobber
link; Unix then syncs the parent directory. Concurrent creators converge on one
complete credential without a persistent lock; crashed temporary-file residue
cannot block restart. Insecure, partial, or malformed target files fail closed.
`CAELIS_CONTROL_TOKEN_FILE` or `--control-token-file` selects another credential
file. Bearer secrets have no command-line flag and therefore need not be exposed
in process argv.

Loopback may use HTTP because the bearer credential is still mandatory. A
non-loopback TCP listener requires a directly configured TLS certificate and
key (`--control-tls-cert` and `--control-tls-key`, or the corresponding
`CAELIS_CONTROL_TLS_*` variables); plaintext bearer transport fails before
listen. Wildcard listeners also require an explicit
`--control-allowed-hosts` allowlist. This version deliberately does not trust
forwarded headers or implement a reverse-proxy bypass.

Before authentication or Service dispatch, the adapter rejects a Host outside
the allowlist, a cross-origin `Origin`, and `Sec-Fetch-Site: same-site` or
`cross-site`. Native/CLI requests may omit browser headers; an explicit browser
Origin must exactly match request scheme and authority. The server emits no
wildcard CORS allow-origin header and never accepts credentials in query
parameters.

Transport status is selected only from typed `agent-sdk/errorcode` values and
cursor sentinels, never from error text:

| Condition | HTTP status | Response rule |
| --- | ---: | --- |
| committed command | `200` | typed `CommandResult` |
| accepted command or effect outcome not provable | `202` | typed `CommandResult`; unknown detail is stable and generic |
| malformed JSON/header/path or typed invalid argument | `400` | validation failure |
| missing/invalid authentication | `401` | generic body plus `WWW-Authenticate: Bearer realm="caelis-control"` |
| authenticated principal lacks Session/action access | `403` | generic forbidden body |
| revision, idempotency, or failed-precondition conflict | `409` | typed command conflict or generic read conflict |
| uncategorized/internal failure | `500` | generic body; internal error text is not exposed |

Control persists only stable public command detail for rejected, conflicted,
or unknown results, so idempotent ledger replay cannot reveal an earlier
backend/store error string. Raw rejected backend failures remain `500`; only an
unknown or conflicted recovery outcome preserves the `202`/`409` retry
contract.

`surfaces/appserver` only enforces request-origin trust, extracts credentials
into a trusted principal, maps DTOs and typed errors, and streams Envelopes. It
contains no Runtime, approval policy, broker, Session store,
participant/handoff policy, or persistence logic.

## Package Ownership

| Responsibility | Owner |
| --- | --- |
| canonical events, child typed origin, paged Session Reader | `agent-sdk/session` |
| Envelope, delivery mode, feed position, cursor codec contract | `protocol/acp/eventstream` |
| ACP semantic projection | `protocol/acp/projector` |
| transport-neutral commands and SessionState | `ports/controlclient` (transitional, frozen) |
| feed broker, durable child recorder, operation store, authorizer, command service | `internal/controlclient` (transitional toward `control/*`) |
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
