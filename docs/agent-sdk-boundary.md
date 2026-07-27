# Agent SDK Boundary

`agent-sdk/*` is Caelis's reusable Agent-building boundary inside the root Go
module. It has no separate module, version, release, or test lifecycle.
Independence means explicit contracts, enforced dependency direction, and reuse
by more than one host; it does not require repository extraction.

## Ownership

| Layer | Owns | Must not own |
| --- | --- | --- |
| Agent SDK | Agent/run values, model and tool contracts, canonical Session semantics, Runtime mechanics, policy and approval primitives, sandbox, task/delegation, normalized ACP-compatible controller and participant contracts | Caelis configuration, credentials, Agent selection, product wire transport, presentation, or Manage Loop decisions |
| Caelis Control | Product configuration and placement, Agent assembly, endpoint lifecycle, permissions and review routing, system Agents, dynamic orchestration, active-controller selection, and handoff commit | Presentation rendering or autonomous model-driven ownership transfer |
| Product ACP | Wire schema, transport, compatibility, normalization adapters, Envelope projection, and documented `_meta` | Agent-selection policy or a second copy of canonical model truth |
| Surfaces | Rendering Envelopes and collecting input | Runtime, persistence, replay, tool, sandbox, permission, or handoff decisions |

Ownership follows semantics rather than directory churn. Stable product
capabilities belong in coherent `control/*` packages, reusable contracts in
`agent-sdk/*`, and private composition glue in `internal/*`.

## Dependency Contract

SDK packages must not depend on:

- `control/*`;
- `app/*`;
- `surfaces/*`;
- product `protocol/acp/*`;
- product-host `ports/*`;
- repository `internal/*` outside the SDK tree.

Product wire and hosts depend inward on SDK contracts, never the reverse.
`make arch-lint` and `make sdk-boundary-check` enforce production and test
dependency closure.

Only paths in
[`agent-sdk/supported-packages.txt`](../agent-sdk/supported-packages.txt) form
the supported external import surface. Other non-`internal` SDK packages are
bundled implementations or experimental helpers until explicitly promoted.
The root Caelis module tag versions the entire SDK.

## ACP-Native Semantics

Built-in and external Agents expose the same effective language for:

- Session and participant identity;
- capabilities and configuration;
- prompt and content input;
- message, thought, tool, plan, permission, and lifecycle updates;
- cancellation, completion, controller, and handoff facts.

Native ACP means semantic equivalence, not mandatory JSON-RPC for in-process
Agents. SDK contracts own normalized semantics; product ACP packages own wire
encoding and compatibility. External input is normalized before it can enter
durable state.

Remote is a transport choice, not a separate Agent category. The SDK does not
adopt Agent-as-tool, Handoff, Workflow node, or Remote Agent bridge as required
top-level abstractions.

## Runtime Contract

Hosts inject model, tool, Session, sandbox, task, policy, and endpoint
implementations. Runtime validates the actual assembled capabilities and fails
closed when a required model, output, tool, or executor feature is unavailable.

A Runner exposes one bounded, single-consumer observation stream. Slow
observers may lose transient output but must not block execution, durable
writes, or completion. Observation gaps are typed and do not change canonical
state.

Agent-loop safety belongs to each Agent implementation at its model boundary.
Control may observe lifecycle and accept explicit cancellation, but it does not
inspect model content or apply a parent watchdog to external ACP controllers,
participants, or third-party children.

Cancellation requested is distinct from proven terminal cancellation.
Similarly, a completed Task control call is not evidence that its target
completed. Unknown side-effect outcomes remain explicit and must not be retried
blindly.

## Control and Handoff

Control alone selects the active endpoint and commits controller-epoch changes.
An Agent may report completion, missing capability, or a suggested next actor,
but cannot authorize its own handoff.

Dynamic orchestration belongs to an event-driven Control Manage Loop. Caelis
does not provide a deterministic workflow graph, node/edge DSL, SDK-owned
Sequential/Parallel/Loop engine, or LLM-facing handoff tool.

Context transfer is recipient-specific and derived from canonical public
Session facts. Tool traces, reasoning, live chunks, routing metadata, workspace
paths, and participant rosters do not become transferred model context merely
because a Surface rendered them.

## Concurrency and Lifecycle

Control fences one canonical Turn per Session across the complete asynchronous
producer lifetime. The fence serializes durable authority, not Agent identity:
local, ACP, and authorized participant Turns follow the same rule.

Overlapping Control writes require an explicitly allowed purpose and the
matching revision or fence. Handoff and controller binding are exclusive.
Unknown mutation purposes, stale revisions, expired leases, and missing guards
fail closed rather than retrying unfenced.

Participant lifecycle is Control metadata with stable identity, delegation,
generation, and revision checks. A parent fence never grants authority over a
different staging Session.

External effects use stable identities and durable intent/effect/terminal
transitions. Identical retries deduplicate; changed payloads conflict;
committed-but-unreported and indeterminate effects remain recoverable or
`unknown_outcome`, never silently repeated.

## Durable Facts and Replay

`session.Event` and guarded Session state are durable truth:

- canonical messages and tool calls/results carry model context;
- typed plan and protocol payloads carry their defined semantics;
- journal facts carry execution and recovery state;
- mirrors are client-facing durable projection, not parent model truth;
- UI, overlay, notice, and raw observation output are transient.

Envelope projection and `_meta` do not replace canonical facts. A semantic
mutation spanning multiple durable values requires an atomic capability;
adapters must not advertise that capability if readers can observe a split
commit.

Persistence contracts require revision CAS, complete-payload idempotency,
fenced writes, monotonic replay ordering, schema migration before typed decode,
and safe restart classification. Unknown durable versions or unverifiable
legacy retries fail closed.

Persistence and replay changes require whole-object round trips proving rebuilt
`[]model.Message` context matches Runtime-produced context. UI reload tests are
not a substitute.

## Stability

The SDK remains a stable dependency only while:

- supported imports compile from an external consumer;
- built-in and external Agents share normalized semantics;
- only Control can select or transfer ownership;
- bounded observers cannot block producers;
- durable context is exactly rebuildable;
- side-effect and lifecycle uncertainty remains typed and recoverable;
- product, wire, and presentation dependencies stay outside the SDK.

Consumer setup and package layout live in
[`agent-sdk/README.md`](../agent-sdk/README.md). Projection-specific rules live
in [ACP Projection Contract](acp-projection-architecture.md).
