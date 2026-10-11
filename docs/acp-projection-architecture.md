# ACP Projection Contract

ACP is Caelis's Agent interoperability language and the payload vocabulary
projected to presentation surfaces:

```text
Agent -> normalized SDK semantics -> Control lifecycle/feed
      -> owner-local ACP projection -> eventstream.Envelope -> Surface
```

Agent and Runtime ownership lives in
[Agent SDK Boundary](agent-sdk-boundary.md).

## Ownership

- `agent-sdk/*` owns reusable message, tool, plan, approval, participant,
  lifecycle, cancellation, and controller semantics.
- `acp-go-sdk` owns standard ACP wire contracts and connections.
- `control/appserver/eventstream` owns the ACP-shaped update and permission
  payloads carried by the Control-to-Surface Envelope.
- `control/appserver/projection` owns canonical Session-event projection.
  Focused Host, external-Agent, and Surface adapters own only their local
  compatibility or presentation projection.
- `control/acppermission` translates standard ACP permission payloads to SDK
  approval semantics; it applies no approval policy.
- Control owns authorization, ordering, replay, approval routing, lifecycle, and
  endpoint selection. Surfaces render and collect input.

Provider input is normalized once before it reaches durable state or the common
Envelope vocabulary. It must not create a parallel semantic schema.

The common narrative projection distinguishes exact append deltas from complete
message replacements. `ContentChunk.replacement` and SDK `ProtocolUpdate.Replacement`
replace the content of the same scoped `messageId`, including empty text, without
settling its Turn. Display batching preserves that boundary and replay retains the
current content. A provider's edited user message is a durable client-replay mirror;
it cannot rewrite admitted native input, model context or an input receipt.
Narrative `replacedFields` recognizes only `_meta`: explicit empty or null metadata
clears its previous display value, while absent metadata retains it. An empty
metadata-only delta leaves the body and final-answer eligibility unchanged.

The final-answer owner retains only the latest introduced assistant identity and
its current text snapshot, plus known provider identities within the Session. A
tool, thought or plan boundary makes that candidate ineligible until it receives
more content. Explicit continuation of the same latest identity resumes it;
updates to older identities repair display only. Anonymous post-boundary v1
content starts a new segment. A new Activity clears the candidate but retains
provider identities and aliases; a complete Session reset clears both. Purely
anonymous generated segments do not accumulate in the Session identity history.

The draft client's existing bounded Session materialization cache supplies a
process-local `PreviouslyObserved` observation when it has already emitted content
for an identity. New Turn and reconnected final-answer owners use that observation
to recognize old display revisions before selecting their first answer. A
metadata-only introduction does not count as emitted content, and updates to an
already selected current identity still apply. This observation has `json:"-"`;
it is neither peer metadata nor durable authority. Resume/replay reconstructs it
through the same materialization cache, whose explicit record and byte exhaustion
errors remain unchanged. Durable model history retains the final answer selected
by its native Turn owner, while provider revisions remain display observations.
The final owner also tracks thought identities introduced in its current Activity,
in a separate namespace from assistant identities. Prior-Activity thought
revisions remain visible without invalidating the current answer; new thoughts
and their same-Activity continuations retain their barrier behavior. Activity and
Session reset both clear this current-Activity set. Anonymous stable-v1 thoughts
retain their original barrier behavior.
Tools follow the same Activity boundary: the draft Session cache retains only
observed tool IDs, using the existing record and byte budgets with explicit
exhaustion. Tool updates and associated terminal projections carry the private
Session observation; the final owner tracks tools introduced in its current
Activity separately from narrative IDs. Old Session tool display repairs do not
invalidate a new answer, while new tools and same-Activity sparse/status/terminal
updates retain their barriers. Neither wire fields nor `_meta` can supply this
observation, and it is omitted from public and durable JSON. Stable-v1 tool
notifications retain their existing behavior.

ACP message and tool-call IDs are opaque Session-scoped strings. Their exact
bytes are preserved through ingress, result selection, canonical storage and
Control live/replay projection; surrounding whitespace is not an alias. Message,
thought and tool namespaces remain separate. Narrative Surface keys use the same
exact message ID without changing native source, request or approval identities.

Tool display patches retain a bounded vocabulary of `replacedFields` so an
explicit empty or null field can clear its previous presentation while an absent
field retains it. Native approval and execution evidence remain independent.
Terminal output deltas append exact bytes; a terminal output projection marked
`replacement: true` carries a complete snapshot, including an empty snapshot.
Neither marker grants permission or changes source identity or lifecycle ownership.

## Envelope authority

An Envelope is a projection and delivery record, not a second Session store.
Canonical Session messages, tools, plans, protocol facts, and guarded state
remain durable truth.

Typed Envelope fields are authoritative for Session/source identity, projection
identity, scope and relation, delivery position, approval identity, lifecycle,
notice kind, and resume Cursor. `Envelope.Cursor` is the only public Session-feed
resume token.

`_meta` is limited to documented display, diagnostics, and bounded
compatibility. It cannot grant authorization, establish ownership, repair
identity, or become an ordering source. Typed fields always win over fallbacks.

Structured tool diffs preserve affected-file operations, exact paths, optional
old paths and file descriptions, and a separately typed rendered patch in live,
canonical and recovery projections. A rendered patch is never complete new file
contents. Legacy whole-file `path`/`oldText`/`newText` diffs keep their meaning.
At a stable ACP v1 boundary, structured diffs become standard text containing
the affected-file summary and supplied patch; public Control clients retain the
structured fields. At the selected draft boundary, legacy whole-file diffs are
converted to affected-file changes and an actual rendered patch. Empty new text
means a cleared file, and does not by itself prove deletion.

## Approval decision context

`approval-subjects-v1` adds one bounded SDK `ApprovalSubject` projection to
permission and review Envelopes (`approval_subject`) and active state
(`subject`). Runtime policy supplies the facts where it interprets the action;
Session preserves them and Control projects the same snapshot for live output,
state recovery and durable replay. TUI consumes that projection. Application
and HTTP clients receive the same contract.

| Subject | Facts supplied by the existing semantic owner |
| --- | --- |
| Command | Command, work directory and normalized execution constraints from native command policy |
| File | Exact candidate paths and read/write operation from native path policy |
| Network | URL and fetch operation when supplied by a network approval owner; this does not introduce a new WebFetch approval gate |
| App access | Ready MCP tool, workspace, declared effect and invocation/session/persistent configuration grant scopes |
| Opaque | Legacy, remote or custom requests without owner-supplied facts |

Negotiated draft permissions enter this same owner through a neutral private
`PermissionContext`: original title, description, subject JSON and native RPC
identity remain canonical review evidence. A command subject supplies only its
defined command and work directory; an absent or unknown subject does not
acquire a tool call, operation or grant. An actual tool call is retained only
when the endpoint supplied it. Public requests may omit `toolCall` and instead
carry bounded title/description and the common subject projection. Uninterpreted
subject input is marked `opaque_input` and withheld from public raw and derived
review text; private evidence and native option resolution remain intact. Draft
schema selection stays at the negotiated endpoint boundary, outside this
stable display contract.

Every field states `known`, `unavailable`, `truncated` or `opaque`. Text is
bounded at 512 Unicode code points, targets at 16 and grant facts at 32; omitted
tails are explicit. No Surface infers operation, target or scope from model
titles, arbitrary input keys or option labels. Grant descriptions join solely
by the original option ID and order. The native request/tool/session identity,
option IDs, labels, kinds and ordering, resolve path, authorization and receipts
remain authoritative. Display facts never grant access or reopen a settled
request. Incomplete context is visible and the TUI selects an offered reject
choice by default when one exists.

Resolution checks the original private permission before queue settlement,
durable publication or execution wake-up. `selected` requires an exact offered
option ID; the original option kind determines allow or deny, and the legacy
`approved` boolean cannot change that meaning. `cancelled` selects no option
and grants nothing. Invalid selections leave the same request pending. Legacy
outcome-only approval and denial remain supported; approvals with an option
contract still require an originally offered allow choice. Older SDK requests
and journals without an option contract retain their outcome/boolean semantics.
Main, child, HTTP and recovered approvals use the same validator.

The approval owner maintains separate canonical review evidence and a display
snapshot. Execution and Guardian retain the exact original action, constraints
and review rationale. Explicit `writeOnly` schema annotations and sensitive
subject fields carry presence only into presentation. Schema traversal supports
local references, composition, object/pattern/additional/unevaluated fields and
array items, with a shared 4,096-operation budget and depth limit of 64. It is a
conservative display redactor, not a schema evaluator or authorization policy;
unclassifiable annotated input becomes opaque. Unmarked arbitrary inputs are
preserved for compatibility and are not claimed sanitized.

When marked input is present, public raw input/output/content and derived
title/reason/justification are withheld because they may quote the value.
`sensitive_input` persists that fact without storing the value, allowing live
and replay review text to be withheld while preserving the typed verdict and
native options. The private pause token keeps action evidence; its separate
`presentation` snapshot supplies client replay. Older journals without a
presentation snapshot retain their existing replay behavior; missing historic
schema annotations cannot be reconstructed safely. Both the retained direct
RuntimeAgent permission callback and Control use the approval owner's display
copy while resolving against the original request.

A projection may claim durable delivery only after storage supplies its Event ID
and Session sequence. Unstored live output is transient; invalid durable claims
fail closed.

## Delivery and replay

- `canonical`: durable Session semantics, model-visible only when the payload
  defines model context.
- `mirror`: durable client projection that is not parent model truth.
- `transient`: disposable observation with no restart guarantee.

Slow or disconnected observers cannot block execution or durable publication.
Control appends normalized delivery records to a bounded file spool without
waiting for a Surface. A valid cursor prefers its exact spool range; if that
cache is missing, expired, or corrupt, Control begins one complete canonical or
final-result replacement. Replacement transport is page-bounded, but valid
Session history has no fixed total replay limit. Replace-capable consumers swap it atomically, while
an ACP or other irreversible consumer rejects replacement after it has exposed
an exact prefix. No gap event or second Runtime stream repairs it.

Retention expiry belongs to an observer's cursor, not to the shared Session
writer. Control captures the accepted durable boundary and spool high-water
together, replaces the expired prefix, then follows from that same cut. Other
observers and later transient events retain the healthy writer. Under continued
pressure, another expired cursor repeats replacement rather than disabling the
Session's spool.

Spool epochs are disposable process caches. After obtaining the exclusive Store
lock, startup reclaims previous epochs in both Session and Task namespaces,
including exhausted caches from earlier versions. This does not alter durable
Session history and requires no manual data cleanup.

Completed reviewed approval decisions have a `mirror` projection from the
Runtime's persisted resolved pause token, including tokens written by earlier
versions. It restores the original tool call's approved or denied display during
replacement and restart. Progress remains transient; private journal metadata,
pending approvals, and invocation receipts are not exposed by this projection.
Replaying a decision neither authorizes execution nor adds model context.
Child decisions arrive on the parent Session feed independently of Task history.
The TUI retains bounded review facts per Spawn anchor and joins them by child
tool call ID when the tool appears, including after pane eviction or replacement.
A review's parent Turn ID never creates a child transcript block.

If the Session spool cannot deliver a live main-Turn terminal, Control may emit
one cursorless `result` append containing only that terminal lifecycle. It is a
bounded completion fallback, not exact history and not a resumable source. If
canonical recovery is available, its replacement commits before this terminal.

The shared Envelope schema therefore makes `cursor` and `position` optional;
the enclosing typed delivery supplies the required context. Control accepts an
exact append only when every Envelope has a valid cursor and position and the
last cursor equals `next_cursor`. A `result` has neither field. Replacement
pages never carry a cursor: Session canonical replacement may retain durable
position as provenance, while Task replacement removes record-local position
because it is not a Session-feed resume source.
The end of a complete Task replacement carries the cursor of its published
spool incarnation and offset. Consumers save it only after replacement commits,
then follow subsequent records in that same incarnation.
Consumers save `next_cursor` after successfully applying the complete delivery;
subscription read-ahead cannot advance their resume position.

Spool registrations are concurrent producer leases. Command completion,
proven Task-producer loss, Task release, participant detach, Session close, and
Host close seal the matching writer; closing a Session lets existing readers
drain. A canonical checkpoint beyond the last delivered durable position means
final catch-up missed a tail; Control replaces the complete view before ending,
since that tail may overlap transient fragments in the exact prefix. A complete
sealed trace ends without replacement. Later attachments are finite canonical reads without
a new spool. Producer admission closes immediately, while physical writer
sealing remains retryable and the registry retains ownership until that seal
succeeds.

Participant detach also follows the Session store's exact-result
`CommittedError` contract. Control captures the Session/Task product address
before mutation, releases its spool writer when the removal committed despite
a reporting error, continues from the returned committed Session, and retains
the warning for the caller.

TUI Session restoration consumes Control-selected history in bounded batches into
an unrendered document. Exact trace deltas remain visible history even when their
delivery mode is transient; durability does not select a second Surface replay
path. The document is published at the feed sync boundary, including replacement
received after live output. Incomplete restoration retains the previous document.
Only the final viewport is laid out; history loading does not animate prior output.
The TUI initially requests the latest two complete Turns, then publishes their
tail without prefetching older history. Scrolling upward requests up to 16 older
Turns through `history_before`; that finite subscription does not change
the live cursor or command target. Pages build privately and prepend only after
completion, preserving the visible block and selection. Failed requests preserve
both the document and the previous history token for retry.

Control indexes source positions and Turn boundaries, never another copy of
content. A window expands when interleaved Turns require earlier records; history
without Turn identities falls back to the complete range. Exact Session spool
positions are indexed as the publisher accepts them. Canonical Session indexing
uses Store provenance metadata when available, resolving unscoped reviewed
decisions through the ordinary projector. Other sources rebuild from their
records after index eviction. Signed backward tokens bind the
Session, optional Task, source incarnation, and upper boundary. Session tokens
arrive at `sync`, child tokens at `replace_end`; absence marks the earliest available boundary. They
cannot be used as live cursors. A lost source rejects the older request rather
than splicing another incarnation into an existing document. Consumers omitting
`history_turns` receive the complete available range, which can be a retained
display window for a child.

Live durable delivery and replay use the same projector. Reload must not create
Session Events, promote transient output, or change rebuilt model context.

## Incremental materialization

Content, lifecycle, accounting, and current state are separate projections. A
terminal state is not another content chunk, and a durable final value is not
implicitly a second live stream.

ACP `tool_call` is a lifecycle snapshot; `tool_call_update` is a sparse patch
keyed by `toolCallId`. Missing fields retain the latest value for that call.
For the stable v1 `name` field, omission and JSON `null` both mean no update;
a string value replaces the name. The same semantics survive persistence and
replay. Experimental v2 nullable-field semantics do not apply to this path.
Terminal status settles the call even without displayable result content and
never reopens on a later sparse update.

Control HTTP/SSE v1 omits the optional `rawInput` display field on `tool_call`
and `tool_call_update` when it contains numbers outside the wire's JavaScript-safe
range. Proposed calls are observable before policy admission, including calls
that policy rejects. Their identity, status, and result still arrive; unsupported
inputs are neither rounded nor stringified for display. This transport omission
does not rewrite canonical model arguments or execution inputs. Permission and
approval-review payloads retain strict numeric validation. Explicitly marked
sensitive presentation values use the approval display boundary described
above; canonical action evidence remains intact.

Assistant, thought, and terminal output chunks are ordered deltas unless they
carry the explicit complete-replacement projection described above. Consumers
append each delta exactly once or apply that replacement. They never compare
text overlap or infer a cumulative value.

A contiguous text run retains one typed message identity. An anonymous prefix may
be promoted when a later chunk supplies the identity for that same run. A
thought/tool/plan/lifecycle/notice boundary ends legacy live append routing;
later anonymous or append-only content starts a new run. An explicit identified
replacement can repair its existing display message across that boundary.

Surface live append targets are run-scoped even when an Agent reuses a message
ID. A typed canonical final repairs the latest matching run in place; this does
not reopen that run or interrupt newer output. Updating an existing tool or
approval row, or a usage gauge, is not a new narrative boundary.

One live Turn has one content source:

- live Assistant and thought text comes from source chunks;
- local command bytes come from Runtime Task observation;
- the canonical final owns the durable complete value and must not repeat live
  Assistant or terminal content;
- explicit `PublishedContent` markers determine which streams were already
  emitted; consumers do not infer ownership from text;
- a native ACP update owns its live content, while its paired canonical Event may
  contribute accounting only.

The Session spool owner retains the typed identities of successfully appended
live narratives until canonical catch-up or Turn completion. Catch-up omits
their already-published content while advancing the complete durable boundary;
accounting and other content still flow. This identity-only bookkeeping is
discarded with the trace and never filters canonical replacement replay.
For main content, the projected source scope retains the canonical Runtime Turn;
the live Control handle's delivery Turn may differ. Catch-up matches source
scope, message identity, and content kind. Terminal cleanup uses the delivery
Turn that admitted the live content.

Fresh replay may materialize a durable complete value once. If retained deltas
and a complete value coexist, Control selects the source by typed message or tool
identity and scope. This is source selection, not text reconciliation.

Standard `usage_update` is a replaceable context gauge, not a token delta.
Caelis keeps the latest main-controller gauge as a typed Session mirror and each
collaborating participant lane's latest gauge on its Task. Accounting includes each latest lane
once and never sums every streamed update.

## Task and child projection

Main-Turn delivery and Task observation are independent:

- the main Runtime producer is synchronously observed by Control before it is
  exposed to a Surface;
- `control/taskstream` owns the authorized directory and observation;
- `control/appserver/taskstream` projects Task records into transient Envelopes;
- Session and Task observation use the same append/replacement delivery
  protocol over independent file-spool partitions.

People and models address Tasks by a Session-unique public handle. Opaque Task IDs
remain correlation values resolved through Control. Child output is observation;
the parent receives one canonical tool result, and child messages, reasoning,
tools, plans, and terminal bytes never become parent model context.

Agent communication is not Task input. `Task write` and Task cancel apply only
to command Tasks that advertise those capabilities. Agents use
`SendMessage {to, message}`, which binds trusted source identity and queues
mail through the [Control mailbox service](external-acp-agents.md). Its mailbox
ID identifies the message, not a Task activity or an ACP delivery acknowledgement.
The Host carries it as `MessageID` on Agent-input projections so a Surface can
recognize the same mail across shared-log observation and direct delivery without
changing mailbox delivery state.
Observed Agent-input replacements and clears retain that same scoped message and
typed sender identity. They update UI-only or mirror display without changing
canonical admitted mail, its model input, or its delivery receipt. Canonical mail
still requires a validated Agent actor and nonempty text.
When the Host dispatches that input, the recipient sees a standard ACP
`user_message_chunk`; display-only sender metadata
lives under `_meta.caelis.agent_communication`. Control derives
`Envelope.AgentCommunicationSource` from the typed event actor; Surfaces use
that field to identify Agent input. External ACP ingress removes the reserved
marker before live or canonical projection. Successful dispatch of a fresh child
prompt emits a producer running observation, so Task activity includes the wait
for its first content update. Queued mail and steering within a running Turn do
not start another activity.

The `subagent-workspace-v1` Host capability covers child input, receipts, layout
preferences, and child model/context descriptors. Interactive attach requires
this capability before opening the TUI.

User prompts from a [participant workspace](participants.md#participant-workspace)
use the authenticated AppServer subagent-input service, independently of Agent
mail and the controller's foreground Turn. Control
pins the Task, participant, child Session, and available attachment generation
before durable enqueue, then rechecks them at dispatch. The shared Runtime child
admission path preserves user provenance; it does not add an Agent sender footer
or copy the prompt into parent context. A queued receipt means pending admission;
`sent` means endpoint acceptance, not model application. Only a proven
non-admission caused by a busy or finishing child remains queued. Uncertain
sending outcomes, including Host restart during dispatch, are recorded as unknown
and are never automatically resent. Operation IDs are immutable and idempotent.
Text and inline image parts share the main prompt's image validation and byte
limits. Control stores the encoded image payload with the queued input, so
delivery does not depend on a Surface-local clipboard file surviving restart.
Unmarked ACP history input is displayed as `user`; Agent mail retains its
header/footer provenance. History without a product principal ID does not invent
one or gain authenticated user authority.

Task status is a replaceable directory snapshot and contains no transcript.
Visible content demand has an independent spool cursor. Child workspaces use
one following subscription, including while idle. A retained display window and
its live tail come from the same Task spool. Opening without a cursor requests
`history_snapshot` and a recent-Turn window. Control streams bounded replacement
pages and commits their continuation cursor at the end; subsequent output starts
at that boundary. Readers behind the retained low watermark receive a replacement.
Backward history tokens page within the retained window; its earliest boundary
may be later than the provider's original Session history.

Control shares one recovery per Task when its cache is unavailable. The runner
checks `loadSession` and consumes ACP `session/load` into a bounded display
projection. The load response is the replay boundary; subsequent live updates
use the same connection and recorder. An idle history open submits no prompt and
defers execution configuration until authorized input. Agent mail and user input
reuse that connection. Recovery uses the same Session Runtime as input admission,
which attached readers retain independently of the parent feed. Each prompt
retains its Host work reference through producer settlement. Built-in managed
children use exact parent/Task authorization for load and resume.

The display projection coalesces adjacent text chunks from the same message and
speaker. The newest two Turns retain detail; older Turns retain user/assistant
messages and Agent communication with sender provenance. Retention is capped at
64 Turns, 4,096 events and 2 MiB, including a single long Turn. Oversized records
and the oldest display prefix may be omitted. The child owns raw persistence;
this projection never supplies model context. At most eight provider replays run
concurrently. Replay does not stage a complete second transcript on disk.

The recorder publishes a private replacement incarnation only after the bounded
projection is written. Subsequent queued updates append to it. Live child output
is compacted at Turn boundaries and byte thresholds through that same publication
path. Compaction preserves retained frames' transport metadata and the latest
lifecycle fact for each retained activity, in source order, including terminal
facts carried separately from dialogue. Readers commit the matching replacement end marker before advancing their
cursor. Failed replacement retains the previous document and reports an error;
a Task final answer never substitutes for child history. Missing raw history is
not reconstructed from the display cache. Command output can use its terminal
`FinalResult` when its cache is unavailable.

Task callbacks admit immutable records into bounded queues; one writer per Task
batches at 64 KiB or a 40 ms flush window. Queue limits include in-flight records:
8,192 records / 32 MiB per Task and 64 MiB across the recorder. Oversized display
records become an omission notice. Queue overload or physical write failure
reports a cache gap without changing execution. Streaming history observers join
their source before returning, including cancellation.

Spool defaults retain at most 16 MiB per stream and 1 GiB globally, including
allocation charges. One-MiB segments roll as a stream grows. Global pressure
reclaims the oldest published windows without waiting for terminal TTL; slow
readers cannot pin disk bytes indefinitely. Reclaimed idle writers can append
again. Unpublished replacement windows are protected from other writers until
publication. Segments are disposable, close without fsync, and are not a durable
message queue. Filesystem errors or a single record exceeding admission limits
remain explicit failures.

Only implemented notification methods enter the ACP client's ordered queue;
standard `session/update`, including negotiated notices, remains enabled.
Both notification count and bytes are bounded. Request/response handling and
cancellation do not pass through this notification filter.

Task descriptors expose the child's assigned or observed model and latest context
gauge. Context counters use decimal strings on the wire. Surfaces never borrow
the parent's model or usage when these child values are unavailable.

The canonical StartThread result closes the creation tool once and exposes the
persistent thread identity. Producer completion owns participant results; the
creation result does not replace them. A completion hint may notify the exact
active parent Run. ReadThread and WaitThread observe public output on demand.
Background Task children receive no StartThread or removal capability. Foreground
participants retain ordinary native delegation capabilities. Task continues
to observe individual Jobs, independently from the Session feed.

Permission requests are Session-feed interactions, not Task frames. Control
publishes a typed approval identity; a Surface returns only that identity and the
user decision. Options are validated before policy selection, and malformed or
ambiguous values fail closed.

A Task control invocation and its target have independent lifecycles. Surfaces
must not render a successful observation as target success or a failed target as
an observer-tool failure.

Participant Turn separators render only after terminal lifecycle with valid
start and end times. Their duration is fixed by those timestamps; running Turns
and completed tools do not create an elapsed-time separator.

## Display and compatibility

Standard ACP `name` carries the programmatic tool name; `title` remains a
human-readable label. Projection uses canonical Runtime tool identity first,
then standard `name`, then retained `_meta.caelis.runtime.tool.name` and the
historical non-standard `kind` fallback. Standard ACP `kind` owns the coarse
tool category. Exact tool names may select a compatible presentation profile
but never execution, permission, persistence, or Task authority.
Native projections retain the tool-name metadata alongside standard `name` for
older clients; remove that compatibility writer when all supported consumers
read the standard field.

Provider-specific metadata is normalized at the Host-private ingress under an
exact maintained profile. Unknown, malformed, or mutability-inconsistent values
stay generic. Provider compatibility must not overwrite a specific standard
kind or executable name.

Display extensions may carry citations, participant addresses, bounded tool
input, and terminal display state. Raw tool results, terminal
bytes, paths, and provider metadata remain presentation evidence.

Every normalized `terminal_output` value is an ordered delta. A compatibility
alias must not also publish a cumulative final. External metadata cannot inject
reserved Runtime wrapper bindings.

## Session and Surface rules

Session ID is the product identity. Workspace, title, path, and `_meta` cannot
repair a missing Session identity.

Product-managed child Sessions are classified only by the exact Host-private ACP
bridge contract and remain hidden from ordinary lifecycle clients. Arbitrary
external metadata is not copied into durable Session ownership.

Every Surface must consume typed Control deliveries, commit replacement pages
only after their end marker, preserve identity/relation fields, keep transcript
state non-durable, treat terminal/approval state monotonically, and avoid
Runtime, policy, Session-store, spool-file, or Host implementation dependencies.

The TUI main transcript folds only Turns restored from Session history replay,
keeping the newest two restored Turns fully detailed. An older restored terminal
Turn displays user and assistant narrative without completed tool, reasoning, or
plan details; a Turn created by the live Session stays fully detailed no matter
how many later Turns begin. Nonterminal main blocks remain fully detailed. This
main-transcript presentation policy does not remove document events. Child panes
also release older detail, periodically compact to 64 Turn blocks / 4,096 events / 4 MiB,
and cap individual displayed text tails. At most eight child documents stay
cached; reopening an evicted pane requests a fresh snapshot and preserves its
draft. These limits never change canonical history or model context. Child
observation retries share a 35-second recovery episode across resolution and
subscription failures; replacement alone does not reset it. Exhaustion keeps the
mounted document and reports an error. Stable following resets the episode. Main-transcript layout materializes
visible blocks and a scroll margin, retaining estimated heights outside that
window. Display-column selections stay tied to measured rows; width changes clear
them before reflow.

Projection changes require whole-Envelope live/replay parity. Changes affecting
persistence or model visibility also require a round trip proving rebuilt model
context matches Runtime-produced context.
