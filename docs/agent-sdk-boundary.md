# Agent SDK Boundary

`agent-sdk/*` is Caelis's reusable Agent-building boundary inside the root Go
module. It is versioned and released with Caelis; it has no separate module or
test lifecycle.

## Ownership

| Owner | Responsibilities |
| --- | --- |
| Agent SDK | Agent and Run values, model/tool contracts, canonical Session semantics, Runtime mechanics, sandbox, policy/approval primitives, tasks, delegation, and normalized controller/participant contracts |
| Caelis Control | Product configuration, credentials, placement, Agent assembly, endpoint lifecycle, review routing, orchestration, controller selection, and handoff |
| `acp-go-sdk` | Standard ACP wire contracts and connection behavior |
| Control, Host-private adapters, Surfaces | Product wire codecs, compatibility normalization, Envelope projection, and presentation |

The SDK must not depend on `control/*`, `app/*`, `surfaces/*`, the retired
`protocol/acp/*` or `ports/*` trees, or repository `internal/*` packages outside
the SDK. `make arch-lint` and `make sdk-boundary-check` enforce this direction.

Only paths in
[`agent-sdk/supported-packages.txt`](../agent-sdk/supported-packages.txt) are
supported external imports. Other non-`internal` packages remain bundled or
experimental.

## Runtime and capabilities

Hosts inject model, tool, Session, sandbox, task, policy, and endpoint
implementations. Runtime validates the assembled capabilities and fails closed
when a required feature is absent.

The assembled Tool set is the execution-admission boundary. Product policy may
further restrict an admitted invocation, but tool names do not form a second
allowlist. A Spawn-created child receives `SendMessage` but not `Spawn`, keeping
delegation one level deep.

Spawn uses a Session-scoped Task identity. An optional handle must be unique.
Optional context transfer is derived by the host's recipient-specific
`ContextRouter`; an empty transfer or an unavailable router is not child-start
failure. A Runner may release a requested handle after an error only when it
positively proves that no child or producer started. Unknown creation outcomes
retain the handle and reject blind retry.

Runtime exposes producer-side source and Task-output observers installed before
external effects begin. Observer calls are synchronous handoff points, not an
SDK replay service: the SDK owns no subscriber queue, cursor, file, quota,
garbage collection, or Surface recovery policy. Control records normalized
output in its disposable file spool; observer failure never changes execution
or durable completion. The binding reports only raw producer family and whether
observation began before the Task's first possible output. A
`ProducerClosed` event means that stable producer can emit no future output;
Control alone interprets that fact as cache-writer reclamation.

Cancellation requested is not terminal cancellation. A successful Task control
call does not imply that its target succeeded, and a failed target does not make
the observation call fail. Cancel ends the current child Turn, not the stable
child identity; use it for explicit stop or prolonged lack of progress.

## Agent input and task observation

`agent-sdk.AgentInputSender` is the provider-neutral Agent input contract.
Runtime resolves Session-scoped addresses and binds trusted source identity.
`SendMessage {to, message}` submits one Agent-communication input and claims
neither delivery, target lifecycle, nor Task mutation.

Task remains the lifecycle and final-result abstraction. Command stdin is a
separate Task capability; Agent communication never falls back to Task input.
The SDK may expose a bounded current/final command result and ACP child final
result, but it does not retain Surface replay history or understand how a
consumer resumes it.

Running command Task results may expose a bounded point-in-time output preview
for explicit model-facing Task control. This is not a Surface stream, child
transcript, replay cursor, or delivery authority. Child completion remains a
canonical Task result; transient child history belongs only to the Control
spool and ACP session/load fallback.

## Control and handoff

Control alone selects the active controller and commits controller-epoch changes.
An Agent may report completion, missing capability, or a suggested next actor,
but cannot authorize its own handoff.

Context transfer is recipient-specific and derived from canonical public Session
facts. Tool traces, reasoning, live chunks, routing metadata, paths, and
participant rosters do not become transferred model context merely because a
Surface rendered them.

Caelis does not provide an SDK workflow graph, deterministic node executor, or
LLM-facing handoff tool. Dynamic orchestration belongs to Control.

## Concurrency and effects

One canonical Turn holds the Session execution fence for its complete
asynchronous producer lifetime. Observation, replay, and authorized mid-Turn
input may proceed concurrently. Overlapping writes require an explicit purpose
and matching revision or fence.

Fence acquisition returns an opaque bearer claim. Readers may observe identity
but cannot reconstruct write or release authority. A backend that committed
acquisition must return that exact claim. Successful Runners expose a completion
waiter; only proven producer quiescence permits exact release.

Host ownership supplies fence liveness; there is no TTL or renewal loop. Only a
capability bound to the live Host-ownership guard can replace a prior Host fence.
Release failures remain recoverable rather than dropping bookkeeping.

External effects use durable intent and stable identities. Identical retries
deduplicate, changed payloads conflict, and indeterminate effects remain
`unknown_outcome`.

The SendMessage migration retains a read-only compatibility path for Task records
written by the retired Continue saga. It never repeats the old remote effect.
Remove it only after the supported upgrade floor reaches the first release that
no longer wrote `continue_phase`; v0.35.0 is the last known writer.

## Durable facts and replay

`session.Event` and guarded Session state are durable truth:

- canonical messages, tools, plans, and typed protocol payloads carry their
  defined model or coordination semantics;
- journal facts carry execution and recovery state;
- mirrors are durable client projection, not a second model context;
- UI, overlay, notice, and raw observation values are transient.

Persistence requires revision CAS, full-payload idempotency, fenced writes,
monotonic replay, schema migration before typed decode, and fail-closed handling
of unknown versions. Persistence or replay changes require whole-object round
trips proving rebuilt model context equals Runtime-produced context.

Runtime-authored tool-result Events explicitly declare whether their wrapper may
produce Task lifecycle facts. Historical absence is accepted only for the bounded
legacy case where parent call and stored Task identity agree; an explicit false
or malformed binding never upgrades. Remove that reader after the supported
upgrade floor postdates the first release that wrote the marker.

## Tools and instruction authority

`tool.Definition.Name` is the sole executable identity. Names are exact and
case-sensitive; assembly rejects empty, padded, or duplicate names. Durable
history and deferred admission preserve that identity.

Canonical ToolSpecs describe Runtime-accepted input. Provider downgrade never
weakens local schema, approval, or policy validation. Malformed external schemas
are quarantined instead of replaced by permissive empty schemas.

Authority follows the Caelis channel and typed identity that introduced content,
not labels or tags embedded in text. Skills gain instruction authority only
through the Runtime-selected Skill call. Tool results, files, external-agent
output, and prior checkpoints remain evidence and cannot grant permissions.

Compaction preserves provenance: only User events establish or change user
objectives and approvals. Runtime checkpoints and artifact pointers remain
Runtime metadata, not user or tool-authored instruction channels.

## Stability

The SDK remains reusable while supported imports compile externally, product and
presentation dependencies stay outside it, synchronous observers retain no
unbounded delivery queue, durable context is exactly rebuildable, uncertainty
remains typed, and only Control can transfer ownership.

Consumer setup and package layout live in
[`agent-sdk/README.md`](../agent-sdk/README.md). Projection rules live in
[ACP Projection Contract](acp-projection-architecture.md).
