# ACP Projection Architecture

ACP has two related roles in Caelis:

1. It is the native interoperability and control language shared by built-in
   and external Agents.
2. It is the common protocol projected to presentation surfaces.

This document focuses on the second role. The reusable SDK boundary and
ACP-native orchestration decisions are documented in
[docs/agent-sdk-boundary.md](agent-sdk-boundary.md).

Caelis presentation surfaces consume standard ACP-shaped payloads plus
documented optional `_meta` extensions. TUI, ACP stdio/server, headless, and
future GUI surfaces should not own runtime, control, tool, sandbox, stream, or
persistence semantics.

```text
Built-in Agent Runtime -------------------------------+
                                                       +-> normalized SDK ACP semantics
External ACP Agent -> transport/lifecycle adapter ----+   -> Control / Agent Manage Loop
                                                            -> eventstream.Envelope
                                                            -> surfaces
```

The control layer may bridge local runtime events, system-managed agent events,
or external ACP-agent updates. Surfaces should not need to know which source
produced an event once it has been normalized into `eventstream.Envelope`.

Native ACP means semantic equivalence, not mandatory JSON-RPC serialization for
in-process Agents. Canonical message and tool payloads remain the model-context
truth; an ACP update or surface envelope is not a replacement for them.

## Semantic and Wire Ownership

`agent-sdk/session.ProtocolUpdate`, `ProtocolApproval`, and their nested DTOs
are the single normalized semantic owner shared by built-in and external
Agents. They contain no JSON-RPC transport requirement and the SDK does not
import the product ACP implementation.

`protocol/acp/schema` owns only public ACP wire shapes, including JSON field
names and patch-style pointer fields. `protocol/acp/semantic` is the adapter
between those wire DTOs and the SDK owner. External ingress decodes through
that adapter before producing session events; projection encodes through it
before adding product display policy or documented `_meta` extensions. This
keeps compatibility, terminal rendering, and transport details outside the
SDK without maintaining a second semantic schema.

## Orchestration Ownership

Built-in and external Agents differ in transport, process lifecycle, trust, and
policy. They do not use different top-level controller or participant semantics.
Control selects endpoints and authorizes handoff; projection only represents
the resulting normalized facts. The full ownership, dynamic orchestration, and
no-workflow rules are defined once in
[Agent SDK Boundary](agent-sdk-boundary.md).

Message/tool/plan updates plus permission, cancellation, participant, and
handoff use the centralized semantic path.
External controller permission ingress and prompt responses route through
`protocol/acp/semantic`; built-in participant and Control-authorized handoff
facts use SDK-owned constructors. Architecture lint rejects new direct
participant/handoff protocol construction outside the SDK semantic owner.

## Task Stream Projection

`RunCommand`, Bash-compatible command tools, and `Spawn` share the task-stream
service, subscription lifecycle, ordering, and backpressure path. They do not
share rendering semantics.

Local command execution projects valid UTF-8 terminal text deltas through the
documented Caelis extension. Sandbox session storage keeps the underlying raw
bytes; live text ingress reassembles split UTF-8 runes independently for stdout
and stderr and replaces genuinely invalid byte sequences explicitly:

- `_meta.terminal_info`: local terminal identity for a tool call;
- `_meta.terminal_output`: the exact retained text delta in `data`, without
  cumulative-snapshot overlap guessing;
- `_meta.terminal_exit`: local terminal termination state when known.
- `_meta.caelis.runtime.stream.truncated=true` plus `truncated_before`: the
  requested byte cursor predates Runtime's bounded live buffer.

The current empty `content[type="terminal"]` anchor is not an output transport;
the Caelis metadata carries the bytes. This is a deliberate compatibility
projection that has been observed to mount and update correctly in the tested
Zed version. It does not claim standard terminal ownership because the official
ACP terminal flow uses `terminal/create` to execute the command in the Client
environment and the Client then owns output, wait, kill, and release.

Forcing an existing Caelis Runtime command through that standard flow changes
execution placement, sandbox, permission, environment, and recovery ownership.
The compatibility anchor therefore remains supported in the current release.
The T0 tradeoff experiment is deferred to a compatibility-focused version and
will select profiles for Zed interoperability, strict ACP, and the Caelis
client protocol. Strict ACP mode may only emit a standard terminal anchor for a
real client-created terminal.

The current repository does not support that client-hosted execution path end
to end. Outbound terminal RPC callbacks exist, but `RunCommand` remains bound to
the SDK sandbox/task lifecycle and the ACP Prompt path does not select the
remote Client as its execution backend. Caelis ACP Client connections therefore
advertise `clientCapabilities.terminal=false` unless a complete terminal
handler is explicitly installed. The reverse local-output adapter used by the
Zed compatibility projection does not change that capability assessment.

A later terminal-compatibility review must use one deterministic RunCommand
fixture covering interleaved output, UTF-8 boundaries, ANSI, non-zero exit,
cancellation, and completion. It must compare the current Zed anchor, Caelis
metadata without that anchor, standard tool content updates, and a real
client-owned terminal lifecycle. The decision must retain wire captures and
rendering evidence, name the selected profile per client class, and specify
capability negotiation, fallback, ownership, and a removal or revisit
condition. Until that evidence exists, the current capability declaration and
compatibility projection remain unchanged.

A spawned Agent instead projects its normalized child message, thought, tool,
content, diff, plan, and lifecycle events as standard ACP semantics. Permission
requests are Control interactions: the bridge normalizes them into an SDK
`ApprovalRequest`, and Control emits the permission Envelope rather than a task
stream frame. Caelis Envelope `scope`, `scope_id`, and `parent_tool` fields
associate those payloads with the parent Spawn call and durable task identity.
The Control child recorder first persists semantic child events as
`VisibilityMirror`; their published `delivery.mode=mirror` position is durable
and replayable without entering parent model context. Exact task bytes and
other events with no stored semantic source use `delivery.mode=transient`.
Session canonicalization may remove a redundant `Protocol.Update` when the
same narrative already exists in `Message`; the normalized
`EventScope.ACP.EventType` remains the typed update identity. Durable
projection must use that identity when deciding whether a message or thought
is still streaming, so storage compaction cannot turn token deltas into final
boundaries. A child narrative boundary never closes its parent Spawn panel;
only the parent tool status/result closes that lifecycle.
`parent_tool` records the delegated relationship in either lane. Spawn and Task
stream frames never emit a parent tool terminal/text copy of child activity,
including when a runtime has materialized `Frame.Text` from a semantic child
event. The parent receives one canonical status/result summary when the
delegated stream closes. These are Caelis Envelope extensions, never custom
fields in an ACP update payload root. Envelope-native Surfaces, including a
future GUI, render the same replayable scoped ACP payloads with the components
used for a main Agent.

The standard ACP stdio `session/update` notification carries only `sessionId`
and `update`; it cannot carry the surrounding Caelis Envelope `scope` or
`parent_tool`. Forwarding a scoped child update unchanged would therefore
flatten it into the main Agent transcript in Zed. At this presentation boundary
only, the ACP bridge uses the typed Envelope relation to render child narrative,
tool activity, plan, and nested terminal text as `_meta.terminal_output` updates
for the already-mounted parent Spawn terminal. It suppresses the corresponding
bare child update on that wire path. The child narrative `final` marker does not
emit `terminal_exit`; the canonical parent Spawn status/result supplies the one
terminal close. This lossy compatibility rendering is neither replay authority
nor a second relation path: it never derives ownership from `_meta`, while the
Control feed and durable child mirror retain the original structured semantics.

A completed main-scope Task wait remains a model-visible canonical result even
when the physical task panel belongs to an earlier Spawn call. Its canonical
tool output carries `target_kind`, `parent_call`, and `parent_tool`; durable
projection promotes that ancestry to typed Envelope `parent_tool`. Surfaces use
the typed relation to complete the original Spawn panel and consume the
observer result instead of rendering a second physical panel. They never
recover this relation from `_meta` or a Surface-private replay path.

`internal/controlclient/turningress.Broker` owns the shared per-Turn fan-in. The
Gateway Control client backend and the transitional ACP/TUI adapter both use
that implementation; the adapter's former live-feed broker is now only an
alias. Turningress derives `StreamRequest` values from main ACP tool updates,
deduplicates by `StreamRequest.Key`, and uses cursor-owned
`stream.Service.Read` snapshots to project each source frame through
`projector.ProjectStreamFrame`. The Control Session Feed Broker attaches to
that ingress and gives TUI, headless, the ACP bridge, and app-server independent
subscriptions to the same ordered Envelopes. No Surface discovers a second task
stream from a rendered update.

The main update is accepted before its task delivery starts; each task source
retains its own cursor/event order; and a projected child semantic event remains
before the one parent final status/result update when its source closes.

One physical task stream has one stable source identity even when a Turn emits
both the original `Spawn`/`RunCommand` update and later `Task wait` observers.
A same-Turn observer never opens another reader. If a detached task is observed
by a later Turn, that broker promotes the observer to the original typed parent
tool/call identity and re-reads from a replay-safe zero cursor: Runtime's current
cursor is not proof of what Control accepted. Durable child-origin idempotency
suppresses the already recorded prefix and publishes only the missing suffix;
the shared Session feed likewise reconciles RunCommand byte ranges by physical
task/terminal identity and absolute output cursor, trimming only an already
accepted replay prefix while preserving identical bytes at a later cursor.
RunCommand therefore keeps retained terminal text, ANSI, UTF-8 boundaries,
cursor, and exit state. If the bounded Runtime buffer has evicted an earlier
prefix, the typed truncation boundary is projected and Surfaces show that gap
explicitly.
Repeated source-read failures are bounded. The broker cancels the owning
Runtime handle once, continues draining its authoritative `ACPEvents` until the
producer closes and releases its execution lease, then performs the final
source barrier and publishes one typed main error plus `failed` terminal.

The current running-task stream anchor is a private, transitional Control input
parse of the source tool update's runtime task metadata. It is confined to
live-delivery discovery; it does not restore metadata-based `ParentTool` or
`Delivery`, and it does not supply correlation, ordering, or durability facts
to a Surface. A future typed running-task anchor or small Control DTO should
replace this parse rather than extending the metadata path.

A Turn feed owns only live client delivery. A matching main lifecycle terminal
first prevents new sources, requests one immediate final `Read` from each
already-started source, and advances a source cursor only after its complete
snapshot has been accepted by the broker. It then drains all accepted batches,
waits for those source acknowledgements, ends the broker delivery contexts, and
emits the one final Turn frame. The final read flushes only frames materialized
at that instant; it neither waits for nor cancels an asynchronous Spawn or
other task runtime work. Task-runtime lifecycle remains owned by the
Runtime/Control task plane. Scoped
subagent/participant lifecycle envelopes close only their own displayed
execution and cannot terminate the parent Turn.

The first-party Session-feed boundary is registered before `BeginTurn`, but its
Turn ingress is attached only after the Surface claims `Turn.Events`. This
prevents fast startup output from being mistaken for a slow consumer during the
period in which `Submit` has not yet returned a stream. Every Session subscriber,
including this internal handoff, has an independent bounded queue and the same
typed slow-consumer disconnect policy. Session publication never waits on a
Surface queue: a paused or abandoned client therefore cannot stop sibling Turn
ingress, durable sequencing, or terminal delivery. The handoff context closes
its subscription, and a disconnected client can resume from its last Cursor.
Once the Surface claims the Turn, `AttachTo` fences its prepared target while
each ingress event and any durable storage gap enter the global Session
sequence. Events accepted during that fence are collected for the target in
their actual acceptance order under hard event and encoded-byte bounds. The
broker releases its lock and durable sequencer before delivering that bounded
batch; only this target delivery may wait, and only through the configured
stall timeout. Expiry disconnects the target with the typed slow-consumer
reason. Target or subscription teardown cancels a blocking target read or
capacity wait, then detaches only that target: the same attachment continues as
an untargeted Session publisher so sibling clients cannot lose the remaining
ingress or terminal. Broker or Turn close releases the complete attachment.
No Session-global lock or sequencer is held during target delivery.

Cancellation does not bypass the Runtime producer barrier. The adapter records
the typed requested outcome, closes the prepared subscription, and cancels the
owning Runtime handle. `AttachTo` drops only its target delivery and continues
publishing the same ingress through the shared Session feed until handle
`ACPEvents` closes after producer and lease completion. Sibling TUI, SSE, and
GUI subscriptions therefore receive the same single terminal state and Turn
identity; the local Turn wrapper crosses that same attachment barrier before
ending. An explicit `Close` remains delivery teardown rather than cancellation
and emits no synthetic terminal.

### Child-scoped permission routing

The Control permission plane gives every live `session/request_permission`
Envelope a typed
`eventstream.ApprovalRequestID`. This is a Caelis Envelope and Control-command
correlation field, never an undocumented `_meta` value, an ACP wire field, or
an eventstream cursor. `control.ApprovalDecision.RequestID` carries the same
typed value back to the owning Turn.

The Session-scoped Control approval coordinator owns the authoritative
registry, FIFO queue, and single active approval head across main, participant,
direct AgentRun, and child origins. A Turn contributes origin and lifecycle
ownership, but it does not own queue ordering. The coordinator reuses an SDK durable pause
token when a Runtime approval has one; otherwise it allocates a stable live ID.
Registration and enqueueing happen before delivery. Only the active head may
be published or resolved; its completion or individual abandonment advances the
next queued request. Parent terminal cleanup releases parent-owned requests but
preserves a detached child's request; Session close releases the complete
coordinator. Unknown, stale, duplicate, and queued-but-not-active responses are
rejected explicitly.

Main Runtime, direct AgentRun, and Spawn-child requests all enter this coordinator as
normalized `ApprovalRequest` values. Control uses their canonical origin and
parent metadata to publish the active standard ACP permission Envelope with the
child `scope`, task `scope_id`, real Spawn `parent_tool`, and unmodified
ToolCall/options/raw input/output/content. The active request and settlement are
durable mirror events, so reconnect needs no second permission route. A user and
Guardian/auto-review are different resolvers of the same active queue head;
auto-review never calls its approver before that request is active.

The ACP child runner no longer emits a permission `Frame.Event`, and
`ProjectStreamFrame` does not project permission frames as an alternate route.
The live broker therefore receives the Control-published Envelope through the
same Turn feed as main and direct AgentRun delivery. TUI, headless, and the ACP prompt
bridge only return that ID plus the user's decision through
`Turn.SubmitApproval`; they do not select an endpoint or own permission policy.

Reconnect resolves a pending live waiter by its durable approval identity.
After a process restart, an orphaned persisted request is swept to a terminal
settlement because Runtime continuation is not recreated. Child mirror updates
remain excluded from parent model context and are never reconstructed from
terminal text.

New `protocol/acp/projector` stream projections write parent relation and
delivery facts only to typed Envelope fields. `eventstream.ResolveRelationDelivery`
owns the one-way typed-first legacy read fallback: each typed pointer is
authoritative when present, and only its absence permits the corresponding
metadata fallback. `surfaces/transcript` is the current production consumer of
that entry point. The ACP prompt bridge forwards the shared Control feed
directly and does not resolve Envelope relation or delivery metadata; the
resolver neither writes metadata nor carries display policy.
The fallback exists only for old replay fixtures and legacy Envelope inputs,
and can be removed after those supported inputs no longer supply the legacy
metadata layout. The Control Session Feed Broker uses typed Envelope fields and
durable positions rather than `_meta` for correlation, ordering, or durability
decisions.

The durable child mirror, delivery mode, feed position, signed resume Cursor,
and app-server boundaries are normative in
[Control Client Protocol v1 — M2 Design](control-client-m2-design.md).

## Session Identity

`session.SessionID` is globally unique within one filestore root. Workspace key
is creation/listing/display metadata and may participate in policy decisions,
but it is not part of session identity.

ACP and gateway surfaces must pass the session id they received and must not
keep in-memory `sessionId -> workspace/cwd` caches to repair later requests.

ACP projection does not create a second persistence authority. Main-controller
and participant prompt streams receive the owning Turn's SDK `MutationGuard`,
and every canonical event materialized by `internal/acpbridge` preserves it on
the Session append. Participant attach/detach is separately classified as
Control-owned lifecycle metadata; controller handoff remains an exclusive,
fenced Control transition. Transport source labels and `_meta` never grant
lease authority.

Before v1.0, unsupported old session/index layouts may fail explicitly. Caelis
prefers the clean identity model over compatibility fallbacks.

Durable State mutations are fenced like event mutations. SDK `StateWriter`
operations are request-scoped and carry expected Session revision plus
`MutationGuard`; file and memory implementations validate the active lease
before invoking an update callback. Legacy ref/state/callback-only writes are
not a supported compatibility path. This prevents configuration, approval
accounting, and future Manage Loop state from becoming an alternate unfenced
semantic writer.
`SnapshotState` remains a pure read even for compatibility documents with a
missing state field: it returns an empty map without persisting repair or
changing Session revision. Persistent repair requires an explicit guarded
mutation.

At the product HTTP boundary, standard ACP update schemas stay closed while
unknown vendor updates use an explicit non-overlapping raw extension variant.
The app-server's production Go JSON shapes are validated against the checked-in
OpenAPI for every request, response, Envelope kind, and ACP update variant. The
generated Go raw object and union also preserve unknown fields through complete
Envelope decode/encode round trips.
