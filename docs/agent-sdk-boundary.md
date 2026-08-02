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

## Tool Schema Contracts

Canonical ToolSpecs describe the Runtime-accepted input set. Conditional
requirements may use JSON Schema conditionals, while the Runtime repeats the
same semantic check and fails closed before execution. A ToolSpec is marked
provider-strict only when its complete schema fits the maintained strict
subset; conditionals are explicitly sent non-strict rather than advertising a
stronger contract than the provider can preserve. Provider downgrade never
removes local unknown-field, type, conditional, approval, or execution-policy
validation.

Dynamic ToolSpecs are cloned and bounded at their semantic ingress. MCP,
ToolSearch, and Spawn/Agent metadata keep separate owners and budgets. Deferred
tool admission occurs before model-visible and durable ToolSearch results so
live execution and replay rebuild the same bounded visible set.

MCP input schemas are recursively validated against the maintained JSON Schema
keyword and value-shape subset before they become ToolSpecs. Malformed types,
containers, conditionals, or unknown keywords quarantine that tool; they are
never replaced with a permissive empty schema. Externally supplied MCP tool and
schema descriptions remain capability metadata, carry an explicit
non-authorizing marker in the provider-visible description, and cannot grant
instruction authority. Normalization preserves accepted property names,
required fields, constraints, and business values.

## Instruction Authority and Runtime Provenance

Authority follows the Caelis channel and typed identity that introduced a
value, not labels embedded in its text. Harness, sandbox, approval, and Runtime
policy contracts remain authoritative for their own boundary. Session,
workspace, global, and Skill instructions apply only through the Caelis path
that selected and injected them; none can grant permissions, weaken approval or
sandbox policy, or override the current user request.

The built-in Skill tool makes a Skill body instruction content only for its
matching Runtime-selected tool call identity. A file, another tool, or an
external participant cannot gain Skill authority by emitting `<skill_content>`
or similar text. Tool and external content otherwise remains evidence, even
when it imitates a privileged tag.

Compaction preserves source authority. Only actual User events can establish or
change user objectives, constraints, approvals, rejections, and corrections.
Typed Runtime events remain authoritative only for their recorded status;
assistant text, tool results, external-agent output, file contents, and earlier
checkpoints are evidence. Compaction input uses Runtime-authored, one-line JSON
source frames; only the top-level source field carries provenance, while every
payload is JSON-quoted so embedded headings, tags, or frame text cannot create
a peer source. Normal and salvage generation use the same framing and authority
contract. A compact checkpoint is stored and overlaid as a
Runtime-authored `Compact`/`System` fact with an explicit non-authorizing
marker. `runtime/chat` may project that fact as a user-role history message for
provider compatibility; the projection does not change its provenance or grant
user authority.

When a large tool result is written to a local artifact, the pointer is Runtime
metadata rather than a tool-authored instruction. The canonical ingress removes
every tool-authored top-level `_caelis` namespace and records a collision in
Runtime-owned Event metadata. Only a successful artifact write may reintroduce
`_caelis.runtime.artifact`; truncation preserves it from an explicit trusted
caller field and never infers trust from Content shape. JSON collisions are
also marked in the Runtime artifact pointer, while the original bytes remain in
the artifact. Text results use a neutral Runtime artifact line. Runtime metadata
must not be merged into a tool-owned `system_hint`. Artifact metadata is
navigation for truncated evidence, not an authorization or policy channel.

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
