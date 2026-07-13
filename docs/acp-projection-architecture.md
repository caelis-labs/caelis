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

Local command execution projects opaque terminal bytes through the documented
Caelis extension:

- `_meta.terminal_info`: local terminal identity for a tool call;
- `_meta.terminal_output`: exact output bytes in `data`;
- `_meta.terminal_exit`: local terminal termination state when known.

The current empty `content[type="terminal"]` anchor is not an output transport;
the Caelis metadata carries the bytes. This is a deliberate compatibility
projection that has been observed to mount and update correctly in the tested
Zed version. It does not claim standard terminal ownership because the official
ACP terminal flow uses `terminal/create` to execute the command in the Client
environment and the Client then owns output, wait, kill, and release.

Forcing an existing Caelis Runtime command through that standard flow changes
execution placement, sandbox, permission, environment, and recovery ownership.
The compatibility anchor therefore remains supported until the T0 tradeoff
experiment selects profiles for Zed interoperability, strict ACP, and the
Caelis client protocol. Strict ACP mode may only emit a standard terminal
anchor for a real client-created terminal. The detailed experiment and removal
criteria live in the roadmap.

The current repository does not support that client-hosted execution path end
to end. Outbound terminal RPC callbacks exist, but `RunCommand` remains bound to
the SDK sandbox/task lifecycle and the ACP Prompt path does not select the
remote Client as its execution backend. Caelis ACP Client connections therefore
advertise `clientCapabilities.terminal=false` unless a complete terminal
handler is explicitly installed. The reverse local-output adapter used by the
Zed compatibility projection does not change that capability assessment.

A spawned Agent instead projects its normalized child message, thought, tool,
content, diff, plan, and lifecycle events as standard ACP semantics. Permission
requests are Control interactions: the bridge normalizes them into an SDK
`ApprovalRequest`, and Control emits the permission Envelope rather than a task
stream frame. Caelis Envelope `scope`, `scope_id`, and `parent_tool` fields
associate those payloads with the parent Spawn call and durable task identity.
`delivery` classifies a live-only transient event separately from the temporary
parent-tool compatibility mirror. `parent_tool` records the real delegated
relationship whether or not a mirror exists. A semantic child event sets
`has_parent_tool_mirror` only when the same source frame also emits an actual
parent terminal compatibility mirror; the parent `tool_call_update` sets
`is_parent_tool_mirror` only when it carries that terminal text. A status-only
parent update is not a mirror merely because its tool is Spawn or Task.
Those fields are Caelis Envelope extensions, never custom fields in the ACP
update payload root. A Surface may derive a compact text panel from those
events, but the formatted text is not the protocol or replay authority. Future
GUI clients render the same scoped ACP payloads with the same components used
for a main Agent.

The parent receives the delegated final result through the canonical Spawn/Task
result. Live child events remain transient until a linked semantic child replay
authority is implemented; they must not be promoted into parent model context
or reconstructed from terminal text.

The completed M1 Control-owned fan-in slice places task-stream discovery and
live delivery in the private `app/gatewayapp/controladapter` live-feed broker.
It derives `StreamRequest` values from the main ACP tool update, deduplicates by
`StreamRequest.Key`, and uses cursor-owned `stream.Service.Read` snapshots to
project each source frame through
`projector.ProjectStreamFrame` before placing it on `gatewayTurn.Events()`.
The main update is emitted before its task delivery starts; each task source
retains its own cursor/event order; and a projected child semantic event remains
before the parent compatibility mirror from the same frame. TUI, headless, and
the ACP prompt bridge consume that one Turn feed and do not discover a second
task stream from a rendered update.

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

### Child-scoped permission routing

The completed bounded M1 permission slice gives every live
`session/request_permission` Envelope a typed
`eventstream.ApprovalRequestID`. This is a Caelis Envelope and Control-command
correlation field, never an undocumented `_meta` value, an ACP wire field, or
an eventstream cursor. `control.ApprovalDecision.RequestID` carries the same
typed value back to the owning Turn.

`internal/kernel.turnHandle` owns the authoritative registry, FIFO queue, and
single active approval head for a Turn. It reuses an SDK durable pause token
when a Runtime approval has one; otherwise it allocates a Turn-scoped live ID.
Registration and enqueueing happen before delivery. Only the active head may
be published or resolved; its completion or individual abandonment advances the
next queued request, while Turn Cancel, Close, and terminal cleanup release all
remaining waiters without injecting a zero-value decision. Unknown, stale,
duplicate, and queued-but-not-active responses are rejected explicitly.

Main Runtime, Side ACP, and Spawn-child requests all enter this coordinator as
normalized `ApprovalRequest` values. Control uses their canonical origin and
parent metadata to publish the active standard ACP permission Envelope with the
child `scope`, task `scope_id`, real Spawn `parent_tool`, transient delivery,
and unmodified ToolCall/options/raw input/output/content. A user and
Guardian/auto-review are different resolvers of the same active queue head;
auto-review never calls its approver before that request is active.

The ACP child runner no longer emits a permission `Frame.Event`, and
`ProjectStreamFrame` does not project permission frames as an alternate route.
The live broker therefore receives the Control-published Envelope through the
same Turn feed as main and Side ACP delivery. TUI, headless, and the ACP prompt
bridge only return that ID plus the user's decision through
`Turn.SubmitApproval`; they do not select an endpoint or own permission policy.

This is live routing, not M2 replay/resume. A durable pause token is already a
reusable SDK identity, but reconnecting to a pending live approval after a
process restart remains M2 work. Parent compatibility mirror removal and ACP
runner trace cleanup also remain open M1 work; the retained mirror is still
transient display compatibility rather than durable parent model context.

New `protocol/acp/projector` stream projections write parent relation and
delivery facts only to typed Envelope fields. `eventstream.ResolveRelationDelivery`
owns the one-way typed-first legacy read fallback: each typed pointer is
authoritative when present, and only its absence permits the corresponding
metadata fallback. `surfaces/transcript` and the ACP compatibility bridge both
use that entry point; it neither writes metadata nor carries display policy.
The fallback exists only for old replay fixtures and legacy Envelope inputs,
and can be removed after those supported inputs no longer supply the legacy
metadata layout. The future Control Event Broker must use typed Envelope fields
rather than `_meta` for correlation, ordering, or durability decisions.

The ordered migration and acceptance criteria are defined in
[Control and Client Protocol Roadmap](control-client-roadmap.md).

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
