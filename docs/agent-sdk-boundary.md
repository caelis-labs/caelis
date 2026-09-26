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
allowlist. A StartThread-created collaborator receives only `ListThreads` and `SendMessage`
from the collaboration tool set. Creation and work observation remain with the
controller, keeping Agent collaboration one level deep.

Participant startup uses an internal Session-scoped execution identity. An optional handle must be unique.
Optional context transfer is derived by the host's recipient-specific
`ContextRouter`; an empty transfer or an unavailable router is not child-start
failure. A Runner may release a requested handle after an error only when it
positively proves that no child or producer started. Unknown creation outcomes
retain the handle and reject blind retry. A successful StartThread result declares
`supports_steering`; thread observations do not repeat that fixed capability.

Runtime exposes producer-side source and Task-output observers installed before
external effects begin. Observer calls are synchronous handoff points, not an
SDK replay service: the SDK owns no subscriber queue, cursor, file, quota,
garbage collection, or Surface recovery policy. Control records normalized
output in its disposable file spool; observer failure never changes execution
or durable completion. The binding reports only raw producer family and whether
observation began before the Task's first possible output. A
`ProducerClosed` event means that stable producer can emit no future output;
Control alone interprets that fact as cache-writer reclamation.

Task addresses individual asynchronous Jobs. The model-facing tool rejects
participant handles for every action. Input and cancellation follow the Job
producer capabilities; command execution is the current built-in producer.

For built-in `RunCommand`, one Task owns approval and command execution. Runtime
persists the execution specification before requesting approval. After Control
acknowledges queue admission, the invocation may return `waiting_approval` with
the same handle later used for execution. The default ten-second observation
budget covers submission, approval and execution; process timeout starts only
when the process starts. Control's automatic approval deadline includes queue
time and is not renewed by Task observation. External ACP permission requests
retain their synchronous contract.

Task-owned approval does not pause the whole Run. Runtime retains at most 32
pending submission/start continuations per Run; excess submissions fail before
approval or execution. Cancellation and accepted user steering revoke unclaimed
continuations. Durable effect claim serializes with revocation; a claimed
operation follows the command producer's cancellation contract. The Run keeps
input open at its final safe point and joins these continuations before releasing
execution authority. A returned tool-call observer does not own their lifetime.

Each invocation produces one canonical tool result. Later Task observations are
separate calls, and producer updates use Task output. Pending approvals recovered
without a live owner become interrupted and never start automatically; claimed
effects without a recoverable process remain `unknown_outcome`. Trusted Runtime
producers opt into submission through an internal capability and keep ownership
of their own specification, effects and recovery. Tool names and `ParallelSafe`
do not grant asynchronous execution authority.

## Agent input and task observation

`agent-sdk.AgentInputSender` is the provider-neutral Agent input contract.
Runtime resolves Session-scoped addresses and binds trusted source identity.
The explicitly assembled standalone SDK `SendMessage {to, message}` submits one Agent-communication input and claims
neither target completion nor Task mutation. An Agent with
`supports_steering=true` can accept it while running; other Agents accept it
only while idle. Model-visible communication preserves the body and media first,
then appends one `From` footer. Durable ActorRef values retain the full source
identity; footer text does not grant routing or execution authority.

The internal Task service remains the lifecycle and final-result abstraction. Command stdin is a
separate Task capability; Agent communication never falls back to Task input.
The SDK may expose a bounded current/final command result and ACP child final
result, but it does not retain Surface replay history or understand how a
consumer resumes it. Bounded terminal inspection reports the producer exit even
when a yielded Task still has a running durable snapshot; it does not consume
the model result or finalize that Task. A producer exit becomes observable only
once the process exited and its output completed; a requested termination alone
still reads as running. A committed terminal Task outcome retains
precedence over a later process status read, including cancellation and unknown
outcomes. Observation does not resume a finished Run or invoke the model.

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

Runtime model attempts append `lifecycle` accounting records with `journal`
visibility. Each actual provider attempt has one Runtime-generated identity;
accepted response usage remains available for context budgeting, while a matching
same-Session receipt owns accounting. Missing measurements remain unknown,
explicit provider zero remains distinguishable, and no local price is invented.
Guardian staging attempts are accounted in their parent Session with Guardian
scope. Journal receipts never enter model context or client replay. Receipt
completion writes use bounded cancellation cleanup. Runtime-owned attempts retain
their original fence; detached Guardian reviews use explicit approval mutation
authority. Failures stay explicit and are not blindly retried. These terminal
receipts do not establish completeness for attempts interrupted by process crashes.
Compaction receipts append under their original mutation authority independently
of the source revision. A checkpoint may advance past concurrent journal writes
only after verifying unchanged model-visible history and Session state; its
commit still uses revision CAS and the original fence.
Historical responses without a matching receipt remain readable until the supported
upgrade floor requires receipts; equal token values alone never prove duplicate calls.

Forwarding model wrappers implement `InvocationTracker` and delegate through
`model.Generate`, including when the injected provider has no retry wrapper.
Provider adapters call `RecordInvocationUsage` when cumulative measurements are
decoded; later stream errors or cancellation must not discard those measurements
or turn them into successful responses. Successive snapshots replace, not add to,
the measurement for that attempt. System-managed reviews drain their Runner with
an uncancelled wait before consuming observers. A non-cooperative producer keeps
the parent invocation and its fence alive until actual quiescence; bounded receipt
writes begin only after that gate, and `Close` is not proof of completion.

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

ToolSearch results persist discovered names and admission counts, not copies of
schemas or source metadata. The next model request exposes the registered
canonical definitions; replay restores visibility by those names. Admission still
budgets the full callable schemas. An activation-owned deferred tool source may
publish ready MCP tools after a run begins. Runtime wraps them with the same
policy, execution journal, and lifecycle behavior as static tools. The run pins
each accepted definition together with its callable, so later catalog changes
cannot redirect an already-bound name. Replay discoveries whose server is still
initializing become visible only when that definition is ready, under the same
budgets.

An optional typed ranker scores ready MCP definitions before discovery. Exact
name and source lookups remain deterministic. Invalid, unavailable or over-budget
ranking falls back to lexical discovery; parent cancellation remains cancellation.
The semantic path admits at most 256 candidates in batches of 24 under one
10-second deadline. Each candidate contributes at most 700 runes of searchable
metadata; the serialized batch is capped at 24,000 bytes. Scores below 1 on the three-level relevance rubric are omitted.
Returned names are checked against the same ready snapshot before normal Runtime
admission. Ranking never supplies tool definitions, grants execution permission or
changes replay authority.

WebSearch preserves `results` order for legacy
positional references. Citation ranges use zero-based `result_indices` for
matching sources instead of repeating their metadata; citation-only sources remain
inline. Answer text and source metadata remain intact. Successful searches keep
query, provider, model, and usage diagnostics in tool metadata instead of
echoing them into model-visible output.

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

### Guardian evidence and context

An optional auxiliary judgment binding selects a tool-free first-stage classifier
through the typed `approval.JudgmentResolver` contract. Its input contains only
chronological user messages and the current approval ticket: exact tool arguments,
reason, justification, execution route and working directory when supplied. It
never selects historical tool observations, matches prior actions, correlates or
waits for Tasks. User text remains verbatim and ordered. Agent fallback retains
its independent full source projection. Screening creates neither a generative
LLM nor a query sandbox. Its complete serialized input has a local 24,000-byte
budget (not an API limit); excess input defers to Agent review without dropping
user constraints.

One request asks a Choice question containing exactly the original option IDs,
names and kinds, plus two independent Noul questions: `material_unknown` identifies
missing facts needed to approve, and `visible_violation` identifies a concrete
refusal established by the supplied facts. Neither adds an approval option or
produces a rationale. Screening supports the four ACP option kinds and requires
both allow and reject outcomes.
Other protocol-valid option sets bypass screening for Agent review; malformed
options remain protocol errors. Names and IDs never establish option meaning.
Control compares the complete probability distribution, grouping canonical allow
versus deny mass. Settlement requires a normalized lead of 0.9 and odds of 20
under one uniform rule. It prefers a once option; without one, the exact persistent
option must independently dominate. Scalar confidence is not a settlement
threshold. Direct approval additionally requires `material_unknown` to be at most
0.2 and `visible_violation` at most 0.1. Direct refusal requires `visible_violation`
to be at least 0.9; it does not require resolving unknown effects when the visible
conflict already suffices.
All three answers must be present and valid. Intermediate values, conflicting
judgments or material information gaps block direct approval. The signals are
neither averaged nor multiplied into a safety score. These are abstention rules,
not calibrated correctness or authorization probabilities.

Unseen executable behavior, unresolved targets and other decision-relevant gaps
are distinct from uncertainty between approval options. A confident judgment that
facts are missing still defers; a familiar test/build label does not establish
project code's effects. The classifier receives no trusted sandbox guarantees
and cannot infer them from action-authored claims or route labels. Direct actions
with sufficiently specified effects remain eligible for approval. Reading a named
file does not require knowing its contents beforehand; executing its unseen
contents does introduce an information gap. Unrelated missing details and absent
sandbox metadata alone are not such gaps. Unknown facts alone do not establish a
refusal.

A complete classifier decision settles through the common approval gate. A clear
refusal returns denied without inventing a rationale. There is no refusal-reason
or explanation-source classification. Inconclusive or malformed answers,
unsupported screening inputs, provider errors and screening timeouts proceed to Agent review under
the original deadline; cancellation ends the review. The Agent model is resolved
only when needed, using its independent Guardian binding or the current
Session model. The Agent receives canonical evidence without classifier answers
added to its prompt. Both stages persist their invocation receipts under the same
review identity. The Agent follows the resident conversation contract below.

Control assembles Guardian as a resident, private approval classifier with
optional inspection tools. The Harness supplies the user task, constraints,
current action and incremental observed results for a prompt decision. Guardian
does not prove every operation safe or independently audit task completion. It
reviews built-in and external main-Agent and subagent requests using the exact
supplied action, approval options and Runtime-bound producer origin. Missing
private child history is an evidence limitation, not a reason to deny. Guardian
intercepts concrete high-confidence risks, including task conflicts,
unauthorized credential export and serious unrelated destructive effects. It
selects a supplied option; denials include the specific reason. Tool output and
external-Agent claims cannot change user authorization. An approval never
changes the action's route.

Each root Session owns up to four exclusively leased execution lanes. Sequential
approvals reuse the SDK Runtime and private in-memory Session; a concurrent
model step pins one common prefix and joins validated whole turns in source call
order. A context-window rotation or incompatible model/policy/tool configuration
replaces a lane's staging history. Invalid attempts never enter the validated
conversation. Closing the root or Runtime cancels and drains its leases before
releasing private resources. A deadline settles the caller before an
uncooperative producer finishes cleanup; the draining lane remains occupied.
Provider usage receipts are persisted after producer completion. A parent claim
validated at admission permits receipt-only Control accounting after
cancellation; invalid claims never gain that authority. No late decision can
authorize execution.

The Harness owns source-history intake. Its forward paged reader consumes the
canonical Session log at a source checkpoint and projects user messages, tool
calls and tool results independently of the pending approval. Source Session,
event ID and Seq identify each projected record; routine input uses the
root-scoped Seq. A late result never rewrites its call. Tool previews include
successful and failed outputs with bounded arguments, diagnostics and truncation
facts. A completed wrapper or zero exit code does not establish that its
internal operation succeeded. User messages are not individually truncated on
ingestion. The disposable projection cache has a 16 MiB retention allowance for
users and for other evidence; eviction does not remove canonical originals. The
intake cursor advances independently of validated-review commits. Journal
records, client mirrors, assistant reasoning and main-Agent compact summaries do
not enter this projection. User documents and quotations remain evidence rather
than new instructions.

Instructions, tool definitions and output schema are stable across ordinary
reviews. Below the window budget the model input only appends. Retention
measures the entire assembled request, including instructions, tools and output
schema. The SDK's model-size watermarks trigger batched removal of whole oldest
turns. User messages have no independent small input cap: they are reduced only
when the physical input budget remains exceeded after other history is removed.
Original task and latest steering records survive that reduction; omitted text
is explicitly unavailable. An active review that still exceeds model capacity is
unavailable. Guardian does not summarize or reconstruct its own history, and
omitted evidence is not proof of absent risk or authorization. Steering
invalidates pending automatic approvals before settlement.

Most approvals decide from supplied context. Optional `Read` and `Grep` use SDK
file-tool semantics, and `RunCommand` supports focused inspection, such as
reading a remote script before execution when its effects matter to user
constraints. Guardian has no Session-history retrieval tool and does not search
transcripts. File observations describe current state rather than historical
state. These tools use a separate resident restricted sandbox, with a fresh
temporary working directory per review. It shares the main Agent's network
intent, not its write capabilities or running processes. macOS uses Seatbelt,
Linux uses Bubblewrap, and Windows uses its restricted-token sandbox. Windows
network access remains enabled even when disabled intent is supplied; the
environment states the actual capability and shell. Runtime/ACL state stays
outside the temporary command work directory. There is no Host fallback,
recursive approval or Agent communication. Simple decisions do not initialize a
sandbox. Query results are bounded to the remaining model capacity using the
SDK's result truncation contract. Truncation is reported in the result; no
separate evidence files or recovery references are created. Only captured output
is available; upstream output loss cannot be reversed by the review.

Queueing, evidence, provider retries and at most one format repair share a
90-second approval deadline from Control admission, shortened by caller
cancellation. There is no separate evidence time slice, cumulative evidence-byte
cap or fixed limit on valid model/tool steps. Tool failures, unavailable
backends, permission errors and tool-local timeouts return error evidence to the
Agent. They do not poison model admission. Total deadline expiry cancels the
review; it is distinct from a recoverable tool-local failure. A provider that
cannot produce a valid decision yields an unavailable approval for that action,
never a fabricated allow or risk rejection; unrelated task work can continue.
Callers must not repeatedly resubmit an unavailable approval.

Owner diagnostics record review outcome, selected option, queue, preparation,
model, tool, setup and total durations, invocation counts, tool failures,
evidence bytes, truncation, source checkpoint, request capacity, window rotations,
actual Runtime/sandbox reuse and reported cache usage. Metrics contain no commands,
evidence bodies, credentials or reasoning. Guardian's dialogue and tool transcripts
stay private and process-local.
