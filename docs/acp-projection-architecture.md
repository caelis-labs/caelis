# ACP Projection Contract

ACP is both Caelis's Agent interoperability language and the payload vocabulary
projected to presentation surfaces. This document defines the projection
boundary. Agent ownership and Runtime rules live in
[Agent SDK Boundary](agent-sdk-boundary.md).

```text
Built-in or external Agent
  -> normalized SDK semantics
  -> Control-owned lifecycle and feeds
  -> owner-local ACP projection
  -> eventstream.Envelope
  -> Surface
```

## Ownership

- `agent-sdk/*` owns reusable messages, tools, plans, approvals, participants,
  lifecycle, cancellation, and controller semantics.
- `acp-go-sdk` owns standard ACP wire contracts and connection behavior.
- `control/appserver/eventstream` owns the ACP-shaped update and permission
  payloads carried by its Control-to-Surface Envelope. Caelis `_meta`
  compatibility is owner-local: Control projection/replay, Host ingress,
  adapter negotiation, and Surface rendering each keep only the codec they
  use. `control/acppermission` owns the standard ACP
  `request_permission` translation to and from normalized SDK approval
  semantics; it applies no approval policy.
- Control owns authorization, lifecycle, ordering, replay coordination,
  permission routing, and endpoint selection.
- The focused Control or presentation adapter creates Envelopes. Canonical
  Session-event projection delivered through the AppServer feed belongs to
  `control/appserver/projection`; owner-local Surface and Host adapters retain
  only their own presentation or compatibility projection.
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

ACP `tool_call` is a lifecycle snapshot, not necessarily a start frame, and
`tool_call_update` is a sparse patch keyed by `toolCallId`. A terminal standard
status on either update settles the same Surface lifecycle even when the update
has no displayable result content. Missing title, kind, input, output, content,
or locations on a patch retain the latest value for that call; they do not
reopen a settled call. Live delivery and replay use this same merge and terminal
status rule.

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
- `control/appserver/taskstream` projects Task records into transient Envelopes.

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
the parent and a Session-scoped Spawn handle addresses a child or sibling. The
tool dispatches an explicit Agent-communication input with a trusted controller
or participant Actor. Runtime stores it as canonical `EventTypeContext` and
projects `caelis/agent_communication`; it is never a User transcript event or a
parent-side audit mirror. Standard ACP targets still use negotiated steering or
`session/prompt` with a user-role-compatible prompt whose trusted sender header
is added by the endpoint owner. Input has no delivery MessageID and does not
mutate Task. Subsequent target output alone advances the Task activity view and
retains its normal ACP output correlation IDs.

Subagent Task output keeps one absolute cursor across observed activities,
independent from `supports_input` and `Task write`. Product content
subscriptions cover one activity period by default. A consumer that explicitly
sets `follow` may release a completed activity's producer observation, wait on
the stable Session/Task identity, and re-resolve it when a later activity
begins. Runtime frames and snapshots carry their concrete ActivityID, and
Control uses that per-record identity instead of the descriptor cached when the
subscription opened. Absolute event/output frontiers are persisted with the
Task so rehydration cannot reset cursor numbering.

Current status is a separate Control capability. A Session directory observer
receives complete, replaceable snapshots and may skip intermediate revisions;
output-only Task commits are folded away. The index retains no transcript or
Task output and is released when the Session's last observer closes. Multiple
Surfaces may watch the same Session independently. No Session-feed wakeup,
parent Final, or tool result controls either stream. The `SendMessage` result is
only an input-dispatch acknowledgement, and `turn_id` only groups transcript
events.

The TUI keeps one lightweight directory observer for its selected Session once
subagents are discoverable. Each visible child overlay independently owns its
content demand. Its live observer follows the stable Task identity across
running and idle activity boundaries, starting from the last stable cursor and
rendering updates through the same transcript blocks used by the main
workspace. If that cursor is older than Runtime retention, the overlay accepts
the current stable boundary and an incomplete running prefix; it does not
synthesize a compaction message. Hiding the overlay closes the live observer
while retaining its Document and cursor. Once a visible live stream delivers a
terminal lifecycle, the TUI starts an independent finite idle read in an
invisible projection and atomically installs it only after a clean,
activity-matched completion; the live observer remains parked for the next
activity. For Agents that advertise `session/load`, that read is the complete
ACP history. A cold terminal overlay starts with only the finite history read
and attaches a live observer when the directory later reports running. If
Spawn failed before creating a child Session,
Control returns retained Runtime current state. An endpoint without
`session/load` uses retained Runtime current state when available and otherwise
the bounded durable terminal fallback described below; neither path claims a
complete prefix. A later running directory
snapshot reopens live content immediately, without waiting for the parent Turn
or Final response. The finite request carries the directory ActivityID and
Control verifies the same Task activity again after the Agent returns; a stale
read is discarded rather than replacing newer live content.
Child-originated Agent communication in the main TUI is one compact
`Received <handle>[<agent>]: <preview>` row rather than a User transcript block.
When its trusted participant identity resolves to a retained Spawn owner, the
whole row opens that child's existing workspace overlay.
Viewing an idle child must not activate an execution Runtime merely to recover
presentation history. On demand, Control reconstructs the exact child endpoint
from the Spawn Task's frozen placement and child Session identity, opens a
short-lived read-only ACP transport, calls `session/load`, and closes that
transport without resuming or closing the durable Session. Built-in and
external ACP children that advertise this capability use the same Agent-owned
history path; Control never reads a child Session file. The complete ACP replay
retains user, assistant, reasoning, tool, and lifecycle updates. A pre-Session
Spawn failure stays on the retained Runtime current-state path. If an Agent
does not support `session/load`, Control also tries retained Runtime current
state; when that Runtime has already been released, it may expose the bounded
durable terminal Task result plus lifecycle as a current-state fallback. That
fallback is not transcript history and makes no completeness claim. Task
directory observation never loads child history: only opening an unresolved
idle workspace or observing a new terminal activity does so. The finite
history read closes after catch-up, while reopening an already settled idle
workspace uses its retained presentation cache. This
projection changes neither Task lifecycle nor parent model context; the
directory remains the current-state authority, and later child output reopens
live observation after input. When a child Session exists, Control also requires
the Spawn Task's frozen endpoint placement; invalid routing fails closed. A
built-in product ACP bridge may honor the exact durable parent/Task relation on
read-only `session/load` only after AppServer principal authorization and a
Host-issued process-scoped history capability. Ordinary ACP Surfaces never
receive that capability. The read does not claim the managed Session and cannot
authorize resume or prompt.

The product ACP parent projection also follows each anchored subagent Task while
that parent Prompt remains active, so a later child activity stays visible
without a second Spawn anchor. Sealing the parent Prompt stops discovery of new
Tasks. An already-running child activity may deliver through its typed terminal;
once the subscription is parked at an activity boundary it closes, so later
activities cannot leak into the completed Prompt. RunCommand remains a
single-activity terminal stream.

Asynchronous child completion may submit one compact ordinary conversation hint
containing state and handle only to the exact active parent Run. It is dropped
when the parent is idle or the Run changes. Final payloads remain owned by the
Task observation path: `Task read` and `Task wait` return every unread retained
activity FinalResponse in
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
`handle` requests a unique Session-scoped Task identity; omitting it keeps
Runtime assignment. Optional Spawn `include_context` asks the host ContextRouter
for public parent context and degrades to the prompt alone when that port is
absent.

Permission requests are Control interactions, not Task frames. Control emits
the approval Envelope on the Session feed after the request has durable
identity. A Surface returns only that identity and the user's decision.
External `request_permission` options are validated before any approval
callback: identifiers are non-empty and unique, and kinds are one of the four
canonical ACP allow/reject values. A registered `danger-full-access` policy
automatically selects a canonical allow option for controller, participant, and
subagent requests; malformed or ambiguous options fail closed, while tool
names, titles, and display metadata never influence that decision.

A Task control invocation and its target have independent lifecycles. A
successful read, wait, command write, or cancel invocation does not prove that
the target completed successfully; target state remains explicit in the
canonical result. A failed, cancelled, or interrupted target likewise does not
fail the observer invocation. Surfaces must not present Wait or Read as a failed
tool panel because the observed command or subagent failed. Likewise, an
accepted `SendMessage` acknowledges routing ownership, not target consumption or
completion of the target Agent Turn.

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

External Agent ingress owns its tolerant update DTOs and unknown-update decoder
inside `internal/acpagentbridge/client`. Standard session-state and permission
shapes prefer `acp-go-sdk`; maintained provider extensions and unknown future
updates remain raw until Host-private normalization has applied trust and
compatibility policy. Only the controller/participant Host boundary projects
those values into `control/appserver/eventstream` for Surface delivery. The
external client never imports Control Envelope DTOs as its wire model.

## Display Extensions

ACP projectors may keep private presentation tables for the exact built-in
names, and Surfaces own their final labels and panel layout. Those tables do
not normalize execution identity: an exact `caelis.runtime.tool.name` may select
the matching UI profile, but it cannot grant invocation, Task, relation, or
persistence authority. Unknown names and former aliases stay generic, and
titles, kinds, arguments, or result fields are never used to infer a built-in
name. Typed `Event.Tool` and Envelope relation fields remain authoritative for
semantic behavior.

Standard ACP `kind` owns the coarse display category independently of the exact
tool name. In particular, `read`, `search`, and `fetch` share the compact
exploration presentation for built-in and external Agents. A specific standard
kind wins over provider metadata. Standard `other` remains the wire-level
generic/default category; a strictly normalized display extension may refine
its derived presentation without rewriting that standard field. The
human-readable `title` is retained separately; for unrefined `other` or an
unknown kind it may be used as the complete generic label, but it never selects
permissions, an exploration category, or an exact tool name. Exact built-in
names may refine a compatible label or result layout only after the standard
kind or a maintained display extension has selected the presentation. Main
transcript blocks, participant blocks, and detached child overlays consume this
one derived presentation model and vary only their container controls.

One maintained Grok compatibility profile is owned by the private external-ACP
client adapter in `internal/acpagentbridge/client`. An inbound `x.ai/tool`
object must use the exact `namespace=grok_build` provider shape before it may
refine a missing standard display category or the presentation of standard
`other`. On a complete tool call, exact provider kinds `read` and `search`
require boolean `read_only=true`, while `edit` and `execute` require boolean
`read_only=false`; only then may the adapter restore that same standard ACP
kind. An omitted kind on a sparse tool update remains omitted, and every
explicit non-generic standard kind wins. Unknown, case-variant, malformed, or
mutability-inconsistent provider kinds remain generic.

The same profile treats exact provider `kind=list` with boolean
`read_only=true` as an anonymous `read` category when the standard kind is
missing. An existing `read` remains `read` and receives the display refinement.
An existing `other` remains `other` but receives the same derived exploration
presentation, while every non-generic explicit standard kind remains unchanged.
The adapter preserves standard raw input and provider metadata, does not add a
non-standard wire kind, and never copies provider `name`, `label`, or `title`
into exact Runtime tool identity. It emits
`caelis.display.exploration_verb=List` only after validating that exact provider
profile. Surfaces accept that normalized hint only for an anonymous `read` or
standard `other` without an exact Runtime tool name, so the hint cannot override
a specific category, independently classify arbitrary metadata, or authorize a
tool. A title beginning with `List` is never sufficient. The category fallbacks
can be removed when Grok emits compatible standard kinds; the display hint can
be removed when standard ACP can represent the List verb without provider
metadata.

Compact exploration rows render path, glob, and search arguments without adding
an outer pair of backticks or double quotes. A matched outer pair recovered from
a provider title may be removed only from that title fallback. Structured ACP
input remains authoritative for the argument itself, including meaningful
boundary quotes or backticks. Shell commands, JSON, narrative text, and embedded
quotes retain their existing semantics.

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
their title. This managed-child and history-capability extension is owned by
the Host-private ACP bridge, not the transitional root metadata package.

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
