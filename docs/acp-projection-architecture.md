# ACP Projection Contract

ACP is both Caelis's Agent interoperability language and the payload vocabulary
projected to presentation surfaces. This document defines the projection
boundary. Agent ownership and Runtime rules live in
[Agent SDK Boundary](agent-sdk-boundary.md).

```text
Built-in or external Agent
  -> normalized SDK semantics
  -> Control-owned lifecycle and feeds
  -> protocol/acp projection
  -> eventstream.Envelope
  -> Surface
```

## Ownership

- `agent-sdk/*` owns reusable messages, tools, plans, approvals, participants,
  lifecycle, cancellation, and controller semantics.
- `protocol/acp/schema` owns public wire shapes.
- `protocol/acp/semantic` normalizes between wire DTOs and SDK semantics.
- Control owns authorization, lifecycle, ordering, replay coordination,
  permission routing, and endpoint selection.
- `protocol/acp/projector` and focused protocol adapters create Envelopes.
- Surfaces render Envelopes and collect input. They do not reconstruct Runtime,
  persistence, replay, permission, or orchestration semantics.

Built-in and external Agents must become semantically equivalent before
projection. Transport or provider-specific input is normalized once at ingress;
it must not create a parallel semantic schema.

## Envelope Authority

An Envelope is a projection and delivery record, not a second Session store.
Canonical model and tool payloads remain durable truth.

Typed Envelope fields are authoritative for:

- Session and source event identity;
- projection identity;
- scope, participant, and parent-tool relation;
- delivery mode and feed position;
- approval identity;
- normalized lifecycle and notice kinds independently from display text;
- resume Cursor.

`Envelope.Cursor` is the only public resume token. Event IDs, projection IDs,
Task cursors, terminal byte positions, and `_meta` values are not substitutes.

`_meta` is limited to documented display, diagnostics, or replay compatibility.
It must never grant authorization, establish durable ownership, repair identity,
or become an ordering or correlation source. A typed field wins whenever both a
typed value and a compatibility fallback are present.

A projection may claim durable delivery only after storage supplies its Event
ID and Session sequence. Unstored live output is transient. Invalid durable
claims fail closed rather than inventing a position.

## Delivery and Replay

- `canonical`: durable Session semantics; model-visible only when the canonical
  payload carries model context.
- `mirror`: durable client-facing semantics that are not parent model truth.
- `transient`: process-local observation with no restart guarantee.

Slow or disconnected observers must not block execution or durable publication.
A lost bounded observation prefix becomes a typed gap or resume boundary.
Surfaces resume through the Control-owned feed and authoritative durable state;
they do not open a second Runtime stream or infer execution failure from a gap.

Replay uses the same projector as live durable delivery. UI reload or transcript
reconstruction must not create Session events, promote transient output, or
change rebuilt model context.

## Incremental Materialization

Content, current state, accounting, and lifecycle are separate projections. A
terminal state such as `completed` is not a content operation, and a durable
final value is not implicitly another stream chunk.

ACP message chunks and Caelis `terminal_output` values are deltas. A receiver
processes them in delivery order and appends each payload exactly once. It does
not compare text, calculate prefix or suffix overlap, merge byte ranges, or
deduplicate a later cumulative value. A producer that publishes a cumulative
final after deltas has violated the projection contract.

One live Turn has one content source:

- Assistant and thought text comes from the live model/source chunks.
- A Caelis-owned `RunCommand` gets terminal bytes only from Runtime Task
  observation through the Control Task stream.
- The canonical final owns the durable complete model value. During live
  projection it may update tool status, terminal exit, lifecycle, and usage,
  but it must not emit Assistant text or `terminal_output` again.
- The source producer marks exact Assistant message, Assistant thought, and
  terminal ownership with the `PublishedContent` value in
  `SourceEvent.CanonicalContentAlreadyPublished`; consumers omit only the
  marked streams and do not infer ownership from text equality or the presence
  of a particular source interface.
- Incremental Assistant and thought chunks carry the stable `messageId` shared
  with their canonical value. An adapter must not guess ownership for anonymous
  chunks from text or scope alone.
- When one source event already carries a native ACP update, that native update
  owns live content and state; the paired canonical event contributes only
  accounting such as `usage_update`.

A fresh replay or `session/load` starts from an empty consumer and may
materialize the canonical complete value once. Live and replay are therefore
explicit projection profiles, never two simultaneous content sources. Resume
cursors remain transport positions for recovering a Task stream; they are not
terminal content semantics and are never used to merge or deduplicate text.
If a captured fresh replay still contains retained deltas beside a durable
complete value, Control selects across the durable storage projection and the
captured ring, preferring the stored complete source by scope-qualified typed
`messageId` or `toolCallId`. Transport Turn IDs are not replay identity; each
later logical message or tool call must rotate its typed ID. A state-only live
supplement at the same durable position cannot shadow the complete stored
projection. If no complete value exists, Control retains the deltas. This is
source selection, not payload comparison or content reconciliation.

## Task and Child Projection

Task observation and main-Turn delivery are separate authorities:

- main-Turn ingress carries only the main Runtime producer;
- the Control Task stream owns Session-authorized Task directory, retained
  observation, and subscriptions;
- the ACP Task adapter projects Task records into transient Envelopes.

People and models address Tasks by a Session-unique public handle. Opaque Task
IDs are resolved through the authorized directory and remain protocol
correlation values. `_meta` is not used for Task discovery or ownership.

Task and child frames are observation. The parent receives exactly one
canonical tool result through the Session path. Child messages, thoughts,
tools, plans, and terminal bytes must not be flattened into parent model
context. Typed scope and parent-tool fields relate them to the owning call.

Agent communication is not Task input. `Task write` accepts only stdin for a
live `RunCommand` launched with explicit `tty=true`; ordinary commands start
with stdin closed and never advertise `supports_input`. `Task write` preserves
the existing line-oriented default by appending a newline, while
`append_newline=false` sends exact terminal bytes for escape sequences and
control input. Spawn Tasks never advertise `supports_input`. Agents use the
runtime-managed `SendMessage {to, message}` tool, where `to=parent` addresses
the parent and a Session-scoped Spawn handle addresses a child or sibling.
The target receives a canonical `Context` event with a typed source Actor, not
a synthetic User event. Explicit `agent_message` metadata or a maintained Agent
message source selects the provider projection, which includes that Actor as
`Agent message from <actor>:`; Actor kind alone is not a routing signal. The
runtime commits a local target Context before wakeup; an idle main Agent consumes
it on its next Turn, while a spawned child whose previous Turn ended with any
terminal outcome may begin a new message-authored Turn on the same stable child
identity. A child-directed message is recorded in the parent Session only after the
delivery runner accepts ownership and only as a mirror, so its body is not
duplicated into main model context. This acknowledgement does not wait for
target consumption. Once delivery ownership transfers, failure to refresh the
sender's Task index returns accepted state `accepted_unpersisted`, not a
retryable delivery error.

Subagent Task output keeps one absolute cursor across message-authored Turns,
independent from `supports_input` and `Task write`. Product subscriptions cover
one activity period by default. A consumer that needs the complete child
workspace sets `follow`; Runtime then releases the completed activity's producer
observation, waits on the stable Session/Task identity, and re-resolves it when
a later message-authored activity begins. Absolute event/output frontiers are
persisted with the Task so rehydration cannot reset cursor numbering. No Session-feed
wakeup event or tool result controls this subscription. The `SendMessage` result
is only a delivery acknowledgement, and `turn_id` only groups transcript events.

The TUI opens a following subscription when the child workspace overlay becomes
visible, catches up from its last cursor, and renders all child events through
the same transcript blocks used by the main workspace. Closing the overlay
cancels only delivery and retains the Document and cursor; reopening resumes.
When the host restarts, viewing a terminal child must not activate an execution
Runtime merely to recover presentation history. Control Task-stream first tries
the live Runtime; if that Session Runtime no longer exists, or its terminal
current-state snapshot reports that the requested assistant prefix was
truncated, it lazily rebuilds assistant-message history from the durable child
Session used by ACP `session/load`. A locally owned child is loaded from the
product Session store. A provider-owned child that is absent there is loaded
through a short-lived read-only ACP transport using the Task's frozen placement
and child Session identity; Control calls `session/load`, closes only that
transport, and never substitutes the Task result for transcript history. Task
directory listing never loads child Sessions: only opening one child workspace
with no resolved in-memory history may subscribe and load that selected child.
The finite historical subscription excludes tool and reasoning history and
closes after catch-up. Because that filtered Session history cannot recover the
original mixed-event positions, Control exposes it as one current-state batch
at the persisted absolute frontier and never renumbers assistant messages into
apparently exact cursors. Reopening a terminal workspace uses its retained
presentation cache. This projection changes neither Task lifecycle nor parent
model context; the Task directory remains the current-state authority, and a
later accepted `SendMessage` reopens live observation.
Terminal lifecycle frames finalize their transcript segment but do not close a
visible following workspace, so a later child Turn appears in chronological
order without exposing Turn identifiers in the UX.

The product ACP parent projection also follows each anchored subagent Task while
that parent Prompt remains active, so a message-authored child Turn stays visible
without a second Spawn anchor. Sealing the parent Prompt stops discovery of new
Tasks. An already-running child activity may deliver through its typed terminal;
once the subscription is parked at an activity boundary it closes, so later
activities cannot leak into the completed Prompt. RunCommand remains a
single-activity terminal stream.

Asynchronous child completion emits one compact Agent Context notice containing
state and handle only. Final payloads remain owned by the Task observation path:
`Task read` and `Task wait` return every unread retained Turn FinalResponse in
chronological order and advance a single parent observation frontier, so a
later observation does not repeat them. Notification never duplicates a child
FinalResponse into the parent transcript. Waiting on multiple comma-separated handles is a
wait-any operation: all waits share one concurrent observation window, a
running/yielded snapshot is not a winner, and the call returns when the first
target becomes terminal, with current snapshots for the remaining handles.
Cancelling those losing observations never changes child lifecycle.
Explicit Task cancel ends the current child Turn without detaching the child
identity. Agents should prefer changing read/wait observations and reserve
cancel for an explicit stop or prolonged lack of progress. Product tool
assembly omits Spawn from every Spawn-created child Session, preventing nested
delegation while retaining SendMessage for parent and sibling communication.
Child system prompts state that they share the parent workspace. Optional Spawn
`include_context` asks the host ContextRouter for public parent context and
degrades to the prompt alone when that port is absent.

Permission requests are Control interactions, not Task frames. Control emits
the approval Envelope on the Session feed after the request has durable
identity. A Surface returns only that identity and the user's decision.

A Task control invocation and its target have independent lifecycles. A
successful read, wait, command write, or cancel invocation does not prove that
the target completed successfully; target state remains explicit in the
canonical result. Likewise, an accepted `SendMessage` acknowledges routing
ownership, not target consumption or completion of the target Agent Turn.

ACP stdio cannot carry surrounding Envelope scope in a standard
`session/update`. For a participant or Side ACP Agent, nested Spawn messages,
thoughts, plans, tools, and notices therefore remain process-local. Each child
Turn may emit at most one terminal update on its parent Spawn call, with the
FinalResponse in standard ACP tool result `content`. This profile does not mount
the parent Spawn as a client terminal, and it never emits that child narrative
as main-agent transcript content, creates a typed nested-child wire
extension, or manufactures a durable parent result. If that standard parent
result already exists in durable participant history, replay renders it through
the same ordinary tool panel and never reconstructs a child workspace.
Product-owned Main Spawn continues to use the typed Task stream and retained
child workspace described above.

## Display Extensions

ACP projectors may keep private presentation tables for the exact built-in
names, and Surfaces own their final labels and panel layout. Those tables do
not normalize execution identity: an exact `caelis.runtime.tool.name` may select
the matching UI profile, but it cannot grant invocation, Task, relation, or
persistence authority. Unknown names and former aliases stay generic, and
titles, kinds, arguments, or result fields are never used to infer a built-in
name. Typed `Event.Tool` and Envelope relation fields remain authoritative for
semantic behavior.

Display extensions may preserve information that standard ACP content cannot
represent without changing semantic ownership:

- structured citations derived from canonical model citations;
- a participant's user-visible address;
- the producing Agent's exact tool name for UI profile selection;
- a normalized safe subset of external tool input;
- local terminal output, exit, truncation, and presentation cursor metadata;
- observation-gap diagnostics.

These values are presentation inputs only. Provider-local references, raw tool
results, transcript text, terminal bytes, or workspace paths must not be
reinterpreted as invocation authority or durable identity.
Maintained provider compatibility is normalized once at ACP ingress: for
example, codex-acp `terminal_output_delta` becomes canonical
`terminal_output` before projection or Surface code. After normalization every
`terminal_output` payload is an ordered delta; a compatibility adapter must not
also publish a cumulative final payload.

External ACP tool metadata cannot carry Runtime wrapper bindings. Ingress drops
the complete reserved `caelis.runtime.binding` section before projection. It
retains an exact `caelis.runtime.tool.name` only as presentation input; no
consumer may treat that value as executable or durable identity.

Standard ACP tool `content` remains the primary display payload. Caelis
advertises `_meta.terminal_output=true` under `clientCapabilities` for the
canonical streaming display extension; the maintained codex-acp
`terminal_output_delta` alias is accepted only as an ingress compatibility
supplement. Canonical and ACP-native projections consume the same normalized
update, so participant transcripts and detached subagent overlays do not own
separate terminal-output interpretations.

The empty terminal content anchor remains a Zed compatibility projection, not
an output transport. Standard ACP terminal content refers to a client-hosted
terminal created through `terminal/create`; Caelis's Shell sandbox executes on
the Agent side and cannot claim that ownership. Client-hosted terminal
execution is unsupported unless a complete handler owns execution, output,
cancellation, and release. A local Runtime command must not advertise
`terminal/xxx` capability or treat the empty compatibility anchor as a second
source of bytes.

## Session and Surface Rules

Session ID is the product identity. Workspace or CWD metadata may guide policy
and display but cannot repair a missing Session ID through a Surface cache.

A built-in ACP subagent classifies its `session/new` request under
`_meta.caelis.runtime.session`. The receiving Runtime promotes only the exact
recognized subagent classification into `system_managed_agent` Session
metadata, preserving parent Session and Task references for diagnostics. The
Control Session directory excludes this product-managed Session from user
resume candidates. Arbitrary ACP `_meta` values are not copied into durable
Session metadata, and legacy unmarked child Sessions are not inferred from
their title.

Every Surface must:

- consume the Control-owned Session feed and Task service;
- preserve typed Envelope identity and relation fields;
- keep transcript and activity state non-durable;
- treat terminal and approval states monotonically;
- keep authentication and DTO mapping thin;
- avoid importing Runtime, policy, Session-store, or product-host
  implementations.

Projection changes require whole-Envelope live/replay parity where applicable.
Changes that affect persistence or model visibility additionally require a
round trip proving rebuilt model context matches Runtime-produced context.
