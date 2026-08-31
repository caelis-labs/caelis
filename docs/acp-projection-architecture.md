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
- `transient`: process-local observation with no restart guarantee.

Slow or disconnected observers cannot block execution or durable publication.
Lost bounded output becomes a typed gap or resume boundary. Recovery uses the
Control feed and authoritative state, never a second Runtime stream.

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

Fresh replay may materialize a durable complete value once. If retained deltas
and a complete value coexist, Control selects the source by typed message or tool
identity and scope. This is source selection, not text reconciliation.

Standard `usage_update` is a replaceable context gauge, not a token delta.
Caelis keeps the latest main-controller gauge as a typed Session mirror and each
subagent lane's latest gauge on its Task. Accounting includes each latest lane
once and never sums every streamed update.

## Task and child projection

Main-Turn delivery and Task observation are independent:

- main ingress carries only the main Runtime producer;
- `control/taskstream` owns the authorized directory and observation;
- `control/appserver/taskstream` projects Task records into transient Envelopes.

People and models address Tasks by a Session-unique public handle. Opaque Task IDs
remain correlation values resolved through Control. Child output is observation;
the parent receives one canonical tool result, and child messages, reasoning,
tools, plans, and terminal bytes never become parent model context.

Agent communication is not Task input. `Task write` is command stdin only when
the command explicitly supports it. Agents use `SendMessage {to, message}`,
which binds trusted source identity and dispatches one input without a delivery
ID, lifecycle claim, or Task mutation. Later child output alone advances Task
activity.

Task status is a replaceable directory snapshot and contains no transcript.
Visible content demand has its own bounded observer and cursor. When exact deltas
are unavailable, Control may supply a typed semantic current-state snapshot with
an explicit gap. A finite idle history read uses the Agent's advertised
`session/load`; Control does not read a child Session file as a presentation
shortcut. Unsupported endpoints may expose retained current state or a bounded
terminal fallback, neither of which claims complete history.

A completion hint may notify the exact active parent Run once, but final output
remains under Task read/wait. Cancel stops the current child Turn without
retiring the child identity. Spawn-created children do not receive Spawn, so
delegation cannot nest.

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
input, terminal display state, and gap diagnostics. Raw tool results, terminal
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

Every Surface must consume the Control Session feed and Task service, preserve
typed identity/relation fields, keep transcript state non-durable, treat
terminal/approval state monotonically, and avoid Runtime, policy, Session-store,
or Host implementation dependencies.

Projection changes require whole-Envelope live/replay parity. Changes affecting
persistence or model visibility also require a round trip proving rebuilt model
context matches Runtime-produced context.
