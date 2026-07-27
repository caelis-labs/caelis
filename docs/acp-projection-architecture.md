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

Permission requests are Control interactions, not Task frames. Control emits
the approval Envelope on the Session feed after the request has durable
identity. A Surface returns only that identity and the user's decision.

A Task control invocation and its target have independent lifecycles. A
successful read, wait, write, or cancel invocation does not prove that the
target completed successfully; target state remains explicit in the canonical
result.

ACP stdio cannot carry surrounding Envelope scope in a standard
`session/update`. Compatibility projection may mount scoped child output on its
own parent tool panel, but must not emit that child narrative as main-agent
transcript content or manufacture a durable parent result.

## Display Extensions

Display extensions may preserve information that standard ACP content cannot
represent without changing semantic ownership:

- structured citations derived from canonical model citations;
- a participant's user-visible address;
- a normalized safe subset of external tool input;
- local terminal output, exit, truncation, and presentation cursor metadata;
- observation-gap diagnostics.

These values are presentation inputs only. Provider-local references, raw tool
results, transcript text, terminal bytes, or workspace paths must not be
reinterpreted as invocation authority or durable identity.

The empty terminal content anchor remains a Zed compatibility projection, not
an output transport. Client-hosted terminal execution is unsupported unless a
complete handler owns execution, output, cancellation, and release. A local
Runtime command must not be advertised as a client-owned terminal.

## Session and Surface Rules

Session ID is the product identity. Workspace or CWD metadata may guide policy
and display but cannot repair a missing Session ID through a Surface cache.

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
