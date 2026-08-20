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
`agent-sdk/*`, and concrete composition glue inside the Host-owned private
implementation boundary.

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
The assembled Tool set is the capability-admission boundary. The built-in
workspace-write policy applies argument- and effect-specific restrictions; it
is not a second Tool-name allowlist. Hosts that need to prohibit an otherwise
assembled Tool omit it from the Agent or register an explicit stricter policy
profile. Caelis product assembly adds `SendMessage` explicitly alongside
`Spawn` for an ordinary Agent. A Session created by Spawn receives
`SendMessage` but never receives `Spawn`, so delegation has one level and a
child cannot create nested children. Runtime augments only a hosted child whose
parent/sibling message transport is supplied through the execution context.
Delegated Spawn defaults to the caller prompt only. Optional `include_context`
asks the host-injected `ContextRouter` for the same recipient-specific public
`ContextTransfer` used by participant handoff. Durable spawn identity binds that
request bit, not the resolved transfer contents; the first intent freezes the
transfer used for child prompt rendering. A missing or failing router is not a
spawn failure: Subagents may be assembled without a router, the child still
starts, and the Spawn result may carry a `system_hint` that context transfer is
unavailable. Runtime hides the argument when no router is assembled.

A Runner exposes one bounded, single-consumer observation stream. Slow
observers may lose transient output but must not block execution, durable
writes, or completion. Observation gaps are typed and do not change canonical
state.

Subagent Task observation intentionally pays for two process-local bounded
views: a 128-frame/1 MiB exact delta window and an ingest-merged semantic
current-state view with a 1,024-unit cap for transient context and a 4 MiB
shared byte budget. Completed Final Messages remain exact, chronological, and
highest priority inside that byte budget; unit-count pressure alone cannot
discard them. Context pressure removes replaceable progress first, then oldest
historical Turn boundaries before current reasoning, tools, or ordinary
assistant content; byte pressure removes Final Messages only after all of that,
from oldest to newest. The latest completed Turn's exact Final is additionally
retained by the Task result contract. This is therefore an approximately 5 MiB
transient per-active-subagent trade-off, plus that latest Final, rather than a
memory reduction from the former single ring.
It prevents an absent or late Surface from determining retention quality while
keeping exact resume cheap inside the short window. Both views remain one
Runtime-owned observation path; neither is durable or parent model context.
Raw `Closed` and terminal `State` frames remain current Task lifecycle
authority. For semantic current-state rebuilds, Runtime retains an older Turn's
completion only as a typed `ui_only` lifecycle event with its completion time;
it carries no Task terminal transport fields and therefore cannot regress the
current Task descriptor. This lets detached child transcripts render the same
per-Turn duration footer after a gap without loading the child ACP Session.
Control delivers a semantic current-state snapshot as one replayable catch-up
batch: partial-record cursors remain anchored before the lost exact window, and
only the final record acknowledges the rebuilt boundary. A disconnect therefore
replays the typed gap and whole snapshot instead of losing an unconsumed suffix.

Agent-loop safety belongs to each Agent implementation at its model boundary.
Control may observe lifecycle and accept explicit cancellation, but it does not
inspect model content or apply a parent watchdog to external ACP controllers,
participants, or third-party children.

Cancellation requested is distinct from proven terminal cancellation.
Similarly, a completed Task control call is not evidence that its target
completed. Unknown side-effect outcomes remain explicit and must not be retried
blindly. Task cancel ends the current child Turn but does not detach or retire
the stable child identity. Its model-facing acknowledgement describes the
subagent as interrupted; the canonical Task state remains authoritative. It is
not the normal way to interrupt an Agent:
prefer read/wait while observations keep changing, and cancel only for an
explicit stop or prolonged lack of progress.

`agent-sdk.AgentInputSender` is the small provider-neutral Agent input contract.
Runtime owns trusted source identity and Session-scoped handle resolution. The
model-facing `SendMessage` tool exposes only `to` and `message`; each call sends
one ordinary input and returns no delivery or target-lifecycle claim. Task
remains the common lifecycle and observation abstraction for commands and
subagents, but Task input is reserved for live command stdin and does not double
as Agent communication. Subagent `Task read` and `Task wait`
do not hold the lifecycle mutation claim while awaiting remote state. A sampled
result is applied only under a short mutation claim and only when its child
activity is still current; observer cancellation never cancels or interrupts
the child.
Each explicit read/wait advances one parent observation frontier and returns all
exact retained activity FinalResponses after that frontier in chronological order;
already observed Finals are not returned again. Spawn itself advances the
frontier when its initial result already contains the first FinalResponse.
While a subagent is running, the model-facing Task result also carries the
absolute `activity_cursor` and a bounded `output_preview`. The preview contains
at most four recent normalized activity blocks within 1 KiB: readable tool
actions with canonical display content, plans, and head/tail slices of active
reasoning or assistant text. It never includes raw tool input/output, is not a
child transcript or replay token, and enters parent context only as this
bounded Task result.

Tool invocation, target execution, and Task observation are separate contracts.
Observer callbacks produce in-progress tool events; a successful SendMessage
result means only that the target input API accepted the call. Input admission
does not claim or advance Task state. Later child output opens or updates one
observed activity generation, and Task read/stream remains an output-derived
view over that stable Session/Task identity.

The input method is selected by the current endpoint owner, not by the caller:

| Input path | Endpoint operation |
| --- | --- |
| Hosted child to an active main parent | Submit ordinary conversation input to the exact active parent Run |
| Hosted child to an idle main parent | Start one ordinary parent Turn |
| Parent or sibling to a running ACP child | Use negotiated `_session/steering` on the exact active activity |
| Parent or sibling to an idle ACP child | Resume when required, then use `session/prompt` on the same child Session |

There is no SDK delivery ledger, mailbox, MessageID, parent audit mirror, or
Task-side input fallback. A completion notice is a separate bounded,
best-effort conversation hint submitted once to the exact active parent Run
after Task and sidecar final are durable. It is dropped when the parent is idle
or the Run changes; failure never delays producer completion or reopens Task
terminal state.

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

The SendMessage migration retains one bounded compatibility reader for Task
records written by the retired Continue saga. It never calls the old remote
effect: prepared records become interrupted, pending or unproven running
post-effect records become `unknown_outcome`, and proven terminal post-effect
records finish their canonical sidecar commit. `v0.35.0` is the last released
writer of `continue_phase`. The first release containing this migration must be
recorded as the first no-write release in its release notes. Remove the reader
only when the documented minimum supported upgrade source is that no-write
release or newer; until then a direct upgrade from `v0.35.0` remains supported.

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

Runtime-authored tool-result Events declare whether their selected wrapper may
produce Task lifecycle facts. `binding.task_result=true` is the live authority;
new ordinary tool results explicitly carry `false`. Historical backfill accepts
an absent binding only for the bounded legacy case whose exact parent call and
stored Task identity agree, and never accepts an explicit false or malformed
binding. Remove that absence-only reader when the minimum supported upgrade
source postdates the first release that writes the explicit marker.

## Tool Schema Contracts

`tool.Definition.Name` is the sole executable tool identity. Agent assembly
rejects empty, whitespace-padded, or duplicate exact names, and execution lookup
is case-sensitive. Built-in tools are an optional assembled preset, not a
registry that claims aliases; an external `SEARCH`, `RunCommand`, or any other
distinct exact name remains an ordinary external capability. Model calls,
durable history, ToolSearch admission, and replay preserve the exact name they
received.

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
