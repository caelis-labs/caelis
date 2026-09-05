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

Live durable delivery and replay use the same projector. Reload must not create
Session Events, promote transient output, or change rebuilt model context.

## Incremental materialization

Content, lifecycle, accounting, and current state are separate projections. A
terminal state is not another content chunk, and a durable final value is not
implicitly a second live stream.

ACP `tool_call` is a lifecycle snapshot; `tool_call_update` is a sparse patch
keyed by `toolCallId`. Missing fields retain the latest value for that call.
Terminal status settles the call even without displayable result content and
never reopens on a later sparse update.

Assistant, thought, and terminal output chunks are ordered deltas. Consumers
append each payload exactly once. They never compare text overlap or reconcile a
cumulative value.

A contiguous text run retains one typed message identity. An anonymous prefix may
be promoted when a later chunk supplies the identity for that same run. A
thought/tool/plan/lifecycle/notice boundary ends the run and clears both content
and active identity; later text starts a new run. Identity is never carried
through a semantic barrier.

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

Fresh replay may materialize a durable complete value once. If retained deltas
and a complete value coexist, Control selects the source by typed message or tool
identity and scope. This is source selection, not text reconciliation.

Standard `usage_update` is a replaceable context gauge, not a token delta.
Caelis keeps the latest main-controller gauge as a typed Session mirror and each
subagent lane's latest gauge on its Task. Accounting includes each latest lane
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
`SendMessage {to, message}`, which binds trusted source identity and dispatches
one input without a delivery ID, lifecycle claim, or Task mutation. The
recipient sees a standard ACP `user_message_chunk`; display-only sender metadata
lives under `_meta.caelis.agent_communication`. Control derives
`Envelope.AgentCommunicationSource` from the typed event actor; Surfaces use
that field to identify Agent input. External ACP ingress removes the reserved
marker before live or canonical projection. Later collaborator output alone
advances Task activity.

Task status is a replaceable directory snapshot and contains no transcript.
Visible content demand has an independent spool cursor. When an exact retained
range is unavailable, Control chooses one complete fallback: command
`FinalResult`, ACP `session/load`, or a descriptor-only running state. A finite
idle history read uses the Agent's advertised `session/load`; Control does not
read a child Session file as a presentation shortcut. A Surface never joins a
partial spool prefix to a fallback or reconstructs content from overlap.

The canonical parent Spawn result closes the parent tool once; child Task
observation never manufactures that result from Task read/wait. A completion
hint may notify the exact active parent Run once. Task read/wait observes final
output on demand; it does not require the parent to wait for every collaborator.
Spawn-created collaborators do not receive Spawn, so collaboration cannot nest.

Permission requests are Session-feed interactions, not Task frames. Control
publishes a typed approval identity; a Surface returns only that identity and the
user decision. Options are validated before policy selection, and malformed or
ambiguous values fail closed.

A Task control invocation and its target have independent lifecycles. Surfaces
must not render a successful observation as target success or a failed target as
an observer-tool failure.

## Display and compatibility

Standard ACP `kind` owns the coarse tool category. Exact Runtime tool names may
select a compatible presentation profile but never execution, permission,
persistence, or Task authority. Human-readable titles are labels only.

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

Projection changes require whole-Envelope live/replay parity. Changes affecting
persistence or model visibility also require a round trip proving rebuilt model
context matches Runtime-produced context.
