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

`AgentSpec.ResolveSessionInstructions` adds embedding-owned guidance to the
stable instruction prefix when Chat assembles a main model request. The resolver
receives the ID of the compaction summary in that request's history. A speculative
candidate has no
effect until Runtime adopts it and Chat builds from the new history. The
optional persist callback runs at provider start after admission accepts the
request. Auxiliary requests use their own assembly or the frozen source request.

`runtime.Config.Hooks` accepts a `hook.Set` from `agent-sdk/hook` and snapshots
its registrations for each Run. Input hooks receive the source, tool hooks
receive detached occurrences and definitions, and model preparation and actual
attempts have separate contexts. Each callback has a name and a timeout
(five seconds by default). Policy and result projection have dedicated slots.
Independent private loops can set `HookPurpose`. The default is `chat`.
System-agent assembly selects `system_agent` and installs its own hooks.

| Phase | Editable value |
| --- | --- |
| Input | Effective Message, system context, early assistant response |
| TurnStart | System context or early response, once per canonical Turn |
| ModelPrepare | Request parameters, configured model choice, durable ContextEdit, cached response |
| ModelInvoke | One actual-attempt continuation, with a detached request |
| ModelResult | Semantic Message; actual usage and provider identity stay fixed |
| ToolPrepare | Final arguments, synthetic result or block, with fixed occurrence and target |
| ToolPolicy | Decide allow, deny or request approval for the final call |
| ToolInvoke | One dispatch continuation after any configured Policy |
| ToolResult | Ordered content blocks, including the optional structured block. Identity, error and system metadata stay fixed. |
| ToolProjection | Bound the presentation and save large canonical results through the configured artifact store |
| ModelView | Tool-result presentation content; the full business value stays fixed |
| Completion | Accept, Replace, Continue with feedback or Reject before final commit |
| Stream factory | Fresh text transformer for one actual attempt or synthetic candidate |

Plain transformations run in registration order on detached values. Around
middleware runs first registration outermost and unwinds in reverse order.
`next` is consumed once and expires at middleware return. A retained or
concurrent second continuation fails without another effect. Hook evidence
excludes raw model handles, progress observers, private call metadata, Session
writers, fence credentials and shared admission barriers.

Pure transformations have bounded outstanding work; timeout discards late
output. Effect middleware retains producer ownership until consumed work
actually drains, including producers which ignore cancellation. Timeout is
not proof of quiescence. Extensions run outside Session write locks. Failures
and panics identify their phase and name. Read-only TraceSink records remain
isolated and bounded, with no power to alter execution.

Input facts retain original Message/source separately from the effective
Message. Steering and Agent communication pass through Input once. Guardian
uses original user evidence. Added context and completion feedback use system
Actors and cannot enlarge user authorization.

Model assembly has a logical identity and increasing assembly revision.
Configuration refresh and context rebuild preserve that identity; semantic
regeneration starts another. Each actual attempt has an independent invocation
ID and receipt. Cache creates neither provider work nor usage. Configured
model alternatives share the snapshot lease and admission owner; hooks cannot
supply raw LLMs or unbound tool schemas. RetryRequest can request reconsideration
of the exact retryable error returned by model next. Existing retry limits,
authority checks, provider-tool restrictions and irreversible-output rules
remain in force. An admitted response retains its snapshot lease through tools.

Every tool route prepares all occurrences and final arguments before the batch
is atomically committed. Policy, approval, canonical envelope and dispatch use
those same arguments. The Session tool fact keeps `proposed_call` as evidence
of the original proposal. Runtime supplies occurrence identity and parentage
through the tool's context. Tools and policy callbacks can read them with
`tool.InvocationFromContext` and `tool.ParentFromContext`. Hook and approval
requests carry detached copies. Identity and parentage do not grant admission.
The Runtime owns model selection, remote target binding, and batch admission.
Transient tool progress is observed separately from the returned result and
stays out of the model history.

A model proposal that fails the tool schema produces a paired error result.
Tool-local execution errors and approval denials also return to the model so
it can correct the call or choose another action. A malformed call envelope
gets a bounded request to regenerate before any call in its batch executes.
Invalid hook transformations fail preparation before batch commit.
Synthetic success passes any configured Policy and the final dispatch checks. Full results
and checked model views share one ToolResult commit, including nested Code Mode
calls. ToolResult affects JS Body; ModelView does not. A failed transformation
commits safe presentation and typed processing_failure separately from the
known effect outcome, then stops. It cannot replay the effect or fall back to
unredacted output.

Buffered output is the SDK default: provider fragments remain private. The
existing final output owner transforms the accepted candidate after ModelResult
and Completion replacements and before canonical publication. Cache and Input
or TurnStart early responses use that same boundary. Their factories identify
`cache`, `input_early` or `turn_start_early` in hook.OutputContext.OutputSource and have
no InvocationID, attempt ordinal, provider receipt or provider usage. Actual
attempt factories identify `provider` and retain their real invocation identity,
including attempts discarded by transport retry. A discarded candidate never
publishes its buffered text.

Incremental modes derive live fragments and final text from one accumulator.
Already transformed text passes through the final boundary as is. Published
text remains authoritative when the provider's final snapshot differs. A
matching snapshot can fill an unstreamed suffix. The accumulator handles empty
chunks and UTF-8 split across chunks. Both modes reject ModelResult and
Completion registrations. Synthetic candidates use the final transformer and
publish once the candidate is accepted.

`StreamIncremental` treats published text as irreversible. Caelis uses
`StreamIncrementalRetry`, which gives a retried request a fresh message ID and
retires the failed attempt's live output through a transient attempt reset.
Observers can clear that message or display the next attempt as a separate
message. The accepted attempt alone supplies the final model response. The
existing automatic retry rules for request completion and provider-executed
tools apply in both modes.

Completion hooks share a feedback budget of two by default. New accepted input
invalidates the old finish decision. Runtime closes final input and drains real
producers before committing final output, then clears its owned Plan and
notifies completion. Cancellation, authority and persistence failures cannot
be replaced by a hook decision.

The built-in chat factory owns these loop phases. External factories declare
the same capability contract with SupportsLoopHooks(); explicitly supplied
Agents and external ACP private loops reject required internal phases before
input mutation. ACP supports input/context transformation, but explicitly
rejects early-response decisions it cannot enforce. The
[external consumer examples](../scripts/testdata/sdk_consumer/hooks_test.go)
exercise retrieval, cache, model selection, early answers, bounded feedback,
effective arguments and independent full/model results without importing
Runtime internals.

Hosts inject model, tool, Session, sandbox, task, policy, and endpoint
implementations. Runtime validates the assembled capabilities and fails closed
when a required feature is absent.

[Code Mode](code-mode.md) binds a fresh-process Goja executor to the existing
Runtime tool chain. Product assembly registers `js_exec` and supplies selected
name/description hints; SDK Runtime owns the generic bounded Session snapshot.
Each execution snapshots current Session tool names for calls by original name. A
known schema needs no search; `tools.search` returns complete schemas or explicitly
refreshes a stale binding. Tools, Session authority and nested execution receipts
retain their existing Go owners. Embedders using the default coding tools must
install the early executable worker entry described in the SDK quickstart.

`sandbox.ExecutionConfig` owns reusable native command environment and shell
configuration. Hosts supply it through `sandbox.Config.Execution`, independently
of CWD and filesystem authority. The product's creation-bound assembly and
restart contract is documented in [Execution Configuration](execution-configuration.md).

The assembled Tool set is the execution-admission boundary. Product policy may
further restrict an admitted invocation, but tool names do not form a second
allowlist. A StartThread-created collaborator receives only `ListThreads` and `SendMessage`
from the collaboration tool set. Creation and work observation remain with the
controller, keeping Agent collaboration one level deep.

Harness Chat executes eligible calls in one native model batch concurrently
when the active model declares parallel tool calls. Batches containing an
unavailable call, a Code Mode submission, or a built-in Patch or Write run in
model order. Runtime Task checks its target handles for script observations.
An adjacent
blocking wait and single-target cancel of the same active script can overlap
in a parallel-capable batch. Cancel does not consume the script result, and
the pair drains before Chat can admit another effect. The exception never
crosses intervening calls. Command Task observations can run concurrently.
Concurrent calls still return tool results in model order. A tool returns ordered content
and reports failure through its error return. Runtime owns invocation identity,
Session tool facts and Run completion.

Independent Code Mode calls can overlap, including calls to the same target
and calls from Chat while an earlier script is yielded. Tool implementations
own synchronization for shared state. Built-in Patch and Write lock the read,
revision check, and write for a shared file across invocations within the
process. Runtime retains per-invocation approval, sandbox and durable effect
admission. Task control retains its per-task mutation claims.

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

Each pending approval belongs to one Session and request ID. The first valid
decision committed through its live owner settles the request. Repeating that
decision is idempotent. An outstanding reviewer receives cancellation, and a
later conflicting answer cannot change the tool outcome.

Task-owned approval does not pause the whole Run. Runtime retains at most 32
pending submission/start continuations per Run; excess submissions fail before
approval or execution. Cancellation and accepted user steering revoke unclaimed
continuations. Durable effect claim serializes with revocation; a claimed
operation follows the command producer's cancellation contract. The Run keeps
input open at its final safe point and joins these continuations before releasing
execution authority. A returned tool-call observer does not own their lifetime.
Recoverable command failures settle on the original Task and are available to
the model through Task observation. A failure to record the outcome still
reaches the Run.

Each invocation produces one canonical tool result. Later Task observations are
separate calls, and producer updates use Task output. Pending approvals recovered
without a live owner become interrupted and never start automatically; claimed
effects without a recoverable process remain `unknown_outcome`. Trusted Runtime
producers opt into submission through an internal capability and keep ownership
of their own specification, effects and recovery. Concurrent dispatch does not
grant asynchronous execution authority.

## Agent input and task observation

`agent-sdk.AgentInputSender` is the provider-neutral Agent input contract.
Runtime resolves Session-scoped addresses and binds trusted source identity.
The explicitly assembled standalone SDK `SendMessage {to, message}` submits one Agent-communication input and claims
neither target completion nor Task mutation. An Agent with
`supports_steering=true` can accept it while running; other Agents accept it
only while idle. Model-visible communication preserves the body and media first,
then appends one `From` footer. Durable ActorRef values retain the full source
identity; footer text does not grant routing or execution authority.

Hosts can discover command inspection and process control through the optional
`task/terminal.Provider` interface. The internal Task service owns the command
lifecycle and final result. Command stdin is a
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

A Task carrying a retired Continue marker has no live input to replay. Recovery
retains a confirmed terminal result or records an unknown outcome, then clears
the marker. Existing Session history is the source of past messages.

## Session storage for embedders

The file Store reads and writes document version 2. Version 1 and embedded
event histories are unsupported. ACP participants require a sealed placement.
A damaged or unsupported Session returns `session.ErrUnreadableSession`.
Recovery leaves an unreadable WAL at its rollout path and continues with other
Sessions. The retained WAL blocks access to its own Session. Shared Store I/O,
locking, and index failures still stop recovery.

A host supplies one `session.Service` to `runtime.Config.Sessions`.
Each `Runtime.Run` selects an existing Session through `RunRequest.SessionRef`
and calls `AcquireWriter` before reading history, binding a controller or writing
input. One producer owns a Session until its tools, steering and
final writes settle. Another prompt for that Session receives a conflict.
Other Sessions can run concurrently, and readers can load committed history
throughout a Turn. Control can delegate an existing execution claim. The
SDK validates it and admits one producer under that claim.

`session.NewService` builds the native service over four backend operations.

| Operation | Backend contract |
| --- | --- |
| `AcquireWriter` | Claim the Session across backend instances and return a write context and release function. |
| `LoadSession` | Read the Session, state and ordered events at one committed revision. |
| `BindController` | Record the initial native controller binding under the writer's authority. |
| `AppendEvents` | Commit an entire batch under the same authority before the first tool executes. |

The service derives individual reads and single-event appends from those
operations. The file and memory implementations also serve the individual
reads directly. Hosts that create and list Sessions, manage participant bindings
and edit state can use `session.ManagedService`. Features such as atomic Plan
updates use their own optional contracts.

Code Mode needs `session.StateWriter` to initialize the `js_exec` catalog for a
new Session. Pass the file store directly as `runtime.Config.Sessions`, or
implement `session.StateWriter` on the service supplied by the host.
`session.NewService(store)` exposes only the four-operation adapter's core
service. Initializing the catalog through that wrapper returns `Unsupported`.

The file backend persists the Turn claim and validates writes under its file
lock. A PostgreSQL adapter can hold the claim in a Session row alongside its
revision and state. Short transactions lock that row to acquire or release
ownership and to append events. Each append checks ownership, revision and
existing event identities, assigns sequence numbers, inserts event rows and
advances the header together. Snapshot reads can use repeatable read. An
identical retry returns the original facts. `session.AcquireFencedWriter` and
`session.PrepareAppendTransaction` share the claim and append semantics.
`session.MarshalEventJSON` and `session.UnmarshalEventJSON` preserve the
durable encoding, including supported schema upgrades and inline media.

Backend implementers can run `sessiontest.ToolRunConformance` to compare the
full model conversation across reopen, and `sessiontest.TurnAdmissionConformance`
to check conflicting prompts and concurrent reads through two runtimes.

## Durable facts and replay

`session.Event` and guarded Session state are durable truth:

- canonical messages, tools, plans, and typed protocol payloads carry their
  defined model or coordination semantics;
- approval, model accounting and compaction facts retain their own owners and
  remain outside model context;
- mirrors are durable client projection, not a second model context;
- UI, overlay, notice, and raw observation values are transient.

ToolCall facts freeze the complete model batch before dispatch, including an
occurrence identity independent of provider call IDs, effective arguments, parent
and target binding. Model history keeps the declared tool name and call ID.
ToolResult stores the complete returned `tool.Result`; its
`Message` is the checked model view. Code Mode nested facts retain complete body
and binding without introducing tool messages the model did not request.

Missing results are repaired only under an exclusive Session fence after the
prior producer has drained or Host ownership has been transferred. Repair appends
an `execution_interrupted` result expressing an unknown outcome, never dispatches
a tool, and preserves every existing result. Uncertain appends are reconciled by
the original event identity and complete payload. A failed reconciliation stops
the caller; it never creates a new operation identity. Read projection places late
results beside their original occurrence and batch. Ambiguous legacy IDs fail
explicitly. Checkpoint coverage never expands to adopt an orphan result.

Run queries describe only live handles. Cancellation and a wait deadline do not
prove producer completion. Plan cleanup follows actual producer drain under the
owning Turn and original fence. Task commands and continuations retain their own
claim, cancellation, output and settlement semantics. Runtime derives command
operation and Spawn identities from the admitted occurrence; `ParentCall` keeps
the original protocol ID for attribution. Direct command callers may supply
`CommandStartRequest.OperationID`; the existing ParentCall fallback remains for
legacy callers until that API requires an explicit identity. A yielded Task
submission already has its single ToolResult; Task completion does not write
another one.

The optional `approval.Requester` receives tool and endpoint permission requests.
`approval.Resolver` settles a pending request by its durable ID.
Approval request/resolution facts live in `Event.Approval`. Runtime owns their
persistence and reusable consent; Control projects their redacted request and
live queue settlement without a second approval history writer. Restart does not
recreate approval waiters. Event schema 4 carries original input evidence and
durable context edits. Resume reads canonical messages and typed tool facts,
including schema 3 results. Retired execution journals and pause tokens are
ignored. An event whose only canonical tool payload lived in that journal can
make its Session unsupported. Future event and approval schemas are rejected.

Runtime releases its Session writer after producers and finalization have
drained. The file and memory services enforce ownership with a Session fence.
Control retains ownership of a fence it delegates to Runtime. A fenced file-backed Task store must
use the same `file.Store` as its Session service; an independently configured
Task store supports unguarded Task operations, not that Session's fence bearer.
Missing-pair repair requires a valid fence even when the service does not expose
fence acquisition.

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
commit still uses revision CAS and the original fence, except for the separate
Store-owned idle checkpoint grant described below.
The experimental `runtime.CompactionConfig.Async` is disabled by default. When
enabled, a fenced native Turn can generate one candidate without holding the
Session write queue. It binds the exact source prefix and coverage sequence,
request instructions and tool schemas, model identity, dynamic snapshot revision,
Session state and controller metadata, and the original fence. Adoption permits
only an unchanged prefix plus a complete, ordered raw tail. Configuration,
or authority changes covered by the embedding snapshot discard the candidate.
MCP catalog refresh is checked at tool dispatch and does not itself mutate a
checkpoint candidate. Current
Runtime continuity is assembled again at adoption. Unpaired tool calls/results,
new checkpoints, and candidates without a smaller projected context are rejected.
Chat binds source coverage when assembling a request, only when its complete
messages equal the observed canonical projection. The actual invocation-start
hook runs after provider admission and image preparation; rejected admission does
not start speculation. Speculation reuses that frozen
request with a final compaction instruction, preserving its instructions, tools,
reasoning, and service tier. An unbound request, changed source hash, or incomplete
tool batch disables speculation. Later canonical appends remain in the raw tail;
the dispatch source boundary may exceed the summarized boundary when a prior
checkpoint follows retained older events. Strict in-context generation validates
both boundaries and fingerprints and never falls back to an independent request.
Custom async compactors must enforce `compact.Request` coverage and in-context
requirements. Synchronous compaction retains its optional fallback.

Model-prepare context edits use the same checkpoint commit owner. Append adds a
system-authored context event with typed source provenance. ReplacePrefix uses
checkpoint contract 2 with purpose `context_replacement`, exact source coverage
and typed replacement messages. Original input, tool, binding and approval facts
remain immutable; Guardian reads original input evidence. Replacement tool groups
must remain complete and retain their original call identities and arguments.
Summary contract 1 remains readable; unsupported event or checkpoint versions
fail closed. Context replacement may increase the model view within its budget;
it does not relax the smaller-prefix and ordered-tail rules for compaction.

Candidate readiness is not a commit. Synchronous, async and idle paths use the
same guarded checkpoint writer, with revision CAS and original-ID readback after
an uncertain write. Only the confirmed canonical compact event changes model
context. Compaction does not retire MCP tools or synchronize an exposure registry.
Dynamic request resolvers supply a side-effect-free
`ModelRequestSnapshot.Validate` guard that serializes embedding configuration and
authority changes with this short commit. Missing validation disables speculation;
it does not fabricate provider admission or modify the Host's last-request record.
MCP availability and grant revocation are checked by their existing execution
owners independently of compaction.

An async candidate stays inside its originating Turn. Stop, turn completion and
synchronous recovery cancel and drain it before Runner completion; existing Host
work references and fence release therefore already cover its lifetime. Dynamic
model snapshot leases are retained until drain. A provider that ignores
cancellation delays completion rather than accessing released resources.
Foreground recovery bounds each actual model generation at 600 seconds by
default through `Async.FallbackTimeout`; retry waiting does not consume the next
generation budget. Speculative candidates retain a finite 600-second owner
deadline through `Async.GenerationTimeout`. Idle candidates use the shared
generation allowance, shortened by the original Store window, and retain their
two-attempt limit. Successful independent cycles have no
Turn-wide quota; the existing durable-progress check stops empty recovery. A ready checkpoint restarts
request assembly from durable truth, so the ordinary budget gate checks the full
summary plus tail and current tool definitions before dispatch. Background usage
receipts never become the foreground context gauge. Uncommitted candidates are
memory-only and are not resumed or inherited by a fork.

Idle compaction has an independent default-off configuration. The Host consumes
an eligible completed request at fence release and owns its one-shot timer and
work reference; the SDK owns source validation, generation and shared adoption.
Idle work acquires an independent resolver lease and admits requests by JobID,
without carrying a completed Turn's admission closure or attributing usage to it.
The dispatch prefix remains immutable. Exactly one canonical final answer may
extend the summarized source; unrelated appends cannot be silently incorporated.
Request-only `model.Request.Source` is excluded from provider JSON.

Session owns checkpoint coverage encoding and validates the frozen handoff proof;
Runtime retains prompt projection and raw-tail selection. The narrow
`IdleCompactionStore` atomically issues a 180-second window with fence release,
then a two-second grant for one fixed checkpoint. Store validation checks the
source stamp, epoch, coverage, fixed payload and revision at the actual commit
point. This capability cannot authorize arbitrary transcript/state writes or
survive foreground acquisition. Unknown writes are read back by original identity.

The Anthropic adapter disables SDK-internal retries; Core owns each attempt so
admission, timing, receipts and idle attempt limits cannot be bypassed.

`CompactionReceiptStore` reserves bounded per-Session accounting before dispatch,
seals a terminal payload and appends only a Store-constructed journal receipt.
Receipt settlement survives foreground revocation and never grants checkpoint
authority. A rejected start-hook reservation is cancelled without inventing an
attempt. An exclusive prior-Host capability can recover sealed receipts or mark
unsealed prior attempts unknown; ordinary Store callers cannot declare a Host
dead. Shutdown retains dependencies if producer drain or settlement is incomplete.
See [experimental features](experimental-features.md) for eligibility and cache
assumptions; these timing constants are not provider-hit or cost evidence.

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
monotonic replay, and version checks on durable facts before replay. Persistence or replay changes require whole-object round
trips proving rebuilt model context equals Runtime-produced context.

Event schema 2 supports event-local sharing of repeated large inline media.
The file Store uses `session.MarshalEventJSON` for JSONL and WAL records; ordinary
JSON encoding retains expanded values. Decoding verifies content digests and
references and restores complete model messages and execution receipts before
validation or recovery. Media never depends on another event or a temporary
artifact, so retaining or discarding an event also retains or discards its media.
Readers replay supported schema-1 records in place. Downgrading to a reader that
predates schema 2 requires a Store backup from before the newer writes.

Task producers own lifecycle state and typed failure diagnostics. Runtime-authored
tool-result Events declare whether their wrapper may supply bounded final-result
enrichment for the same producer terminal state and child activity. Running
observations and state-only receipts do not replace Task results. A later terminal
observation can omit final text; that omission retains the preceding result and
its canonical cursor. A missed sync can recover missing final text from matching
canonical history, including text preceding a newer exit-only receipt; retained
producer fields remain authoritative.

Historical marker absence is accepted only where parent call and stored Task
identity agree; an explicit false or malformed binding never upgrades. The SDK
Task recovery owner retains this compatibility reader until the supported upgrade
floor postdates the first release that wrote the marker. Child history without
activity identity is limited to the first activity.

## Tools and instruction authority

The chat loop bounds each tool result before sending it to the model. Session
replays that recorded view. By default, the loop writes no temporary artifacts.
Hosts configure the budget and optional store in `runtime.Config.Hooks.ToolProjection`.
When a direct result exceeds the budget, the configured store retains the
complete result and the model receives a bounded view with its reference.
Storage failure leaves the bounded view available. Nested Code Mode calls use
the same model budget and return the full business value to the script.
Progress, nested projections and replay perform no artifact writes.

The local Gateway shares one `artifact.FileStore` across its chat agents. It
saves a single text or structured JSON block as-is and uses the result codec's
ordered `content` array for multipart results, preserving media and resources.

`tool.Definition.Name` is the sole executable identity. Names are exact and
case-sensitive; assembly rejects empty, padded, or duplicate names. Durable
history and deferred admission preserve that identity.

Canonical ToolSpecs describe Runtime-accepted input. Provider downgrade never
weakens local schema, approval, or policy validation. Malformed external schemas
are quarantined instead of replaced by permissive empty schemas.

Session replay keeps matched model calls and bounded results. It omits
historical batches with incompatible call and result identities, including
retired MCP routing wrappers. Resuming a Session never dispatches a historical
call.

Code Mode Sessions expose a fixed `js_exec` definition. Its async body can call
known-schema Session tools using original function or method syntax, such as
`await Read(args)` or `await dtw.apps(args)`, without search. Special names and
collisions use the qualified
`tools.native[original]`, `tools.mcp[server][remoteTool]`, or
`tools.plugins[plugin][server][remoteTool]` paths; `tools.call(name,args)` remains
available by canonical name. Every Goja execution snapshots current Session tool
names. Directory pages contain only names and descriptions. Exact name lookup returns a complete schema and refreshes its binding. Discovery is available only in Code Mode.

Code Mode reconstructs MCP bindings from canonical search receipts and typed nested ToolCall facts across fresh workers, compaction
and resume. A
first direct call with no prior binding pins the current real Tool identity/version.
A later change to the bound MCP source/schema fails stale until a successful
`tools.search({name})` explicitly reloads the schema and binding. Search grants no execution permission, and an
unknown effect is never replayed. Manager name-collision losers are absent from the
Session tool set and cannot be restored through qualified paths.

Runtime resolves every route to a real Tool, validates the accepted schema and
arguments, and enters the same policy, Guardian/approval, cancellation, dispatch admission,
invocation owners and typed hooks as other calls. Display and approval data name
the real target and arguments. Calls inside `js_exec` carry their own parent-linked execution facts. The model
receives the result under the outer `js_exec` call ID. Deferred MCP schemas never join model
`tools`, and no per-run exposure limit or retirement tool remains. [MCP tools](mcp-tool-lifecycle.md)
describes refresh and final dispatch. The stable-tools contract does not promise
that the entire model request prefix is unchanged.

The host installs Policy through `runtime.Config.Hooks.ToolPolicy`. Without a
registry or requested profile, Runtime dispatches the host's tools directly
after argument preparation. A named profile requires a matching registered
mode. The Host explicitly registers `danger-full-access` when it authorizes
that mode. Agent metadata selects among the Host’s registered modes. With
Policy installed, direct calls and Code Mode targets share its
Allow/Deny/Ask decision on the effective arguments. An approval resumes the same guarded dispatch, including its final revocation check. A synthetic reply consumes the same admission. The Caelis Gateway explicitly installs its product
policy. MCP discovery and server annotations only describe available tools.

The product's `control/toolpolicy` mode combines native per-call restrictions
with source consent into one decision and approval request. Ordinary MCP offers
Once, Session, Always, and Cancel. Scope uses the authenticated configuration
owner, workspace, exact tool binding and source revision. Session consent also
binds Session ID and creation time. Always writes an exact rule to the existing
AppConfig through the configuration CAS and command ledger, using the original
approval decision identity. Ordinary and application Sessions use that same
owner. Revoked rule identities cannot be recreated by a late approval retry.
A failed or uncertain settlement stops the pending invocation; a committed
configuration mutation is not permission to replay its tool effect.

Session consent is projected from typed resolved approval facts. Legacy pause
tokens and `control/mcp-grants.json` are never imported as permission. The old
file is inert and may be removed after preserving any desired audit copy.
Resource-wide approvals cannot become permanent rules. Policy consumes detached
Session evidence. MCP plugin and server names come from the assembled
configuration. For stdio, the callable fingerprint includes the frozen
effective environment without exposing its values.

Desktop World integration details belong to [Plugins](plugins.md). Runtime only
understands typed approval scope and choice persistence. It shares a pending
pure resource approval among current waiters; per-call risks remain separate.
Leader cancellation leaves surviving waiters active, and the last waiter
cancels the request. Before dispatch, a single-use permit checks cancellation,
revocation, and current Policy validity under a short owner lock. MCP I/O
runs outside that lock.

Code Mode discovery browses the current ready directory without invoking a model.
Directory pages carry only names and descriptions. Exact lookup returns schema
and binding information. On resume, Runtime restores search bindings from
successful Runtime-observed structured results. Older search markers are
ignored. Model calls still pass through current Runtime policy. See [Code Mode](code-mode.md).

WebSearch preserves `results` order for legacy
positional references. Citation ranges use zero-based `result_indices` for
matching sources instead of repeating their metadata; citation-only sources remain
inline. Answer text and source metadata remain intact. Successful searches keep
query, provider, model, and usage diagnostics in tool metadata instead of
echoing them into model-visible output.

Authority follows the Caelis channel and typed identity that introduced content,
not labels or tags embedded in text. Tool and schema descriptions document
capabilities. Skills gain instruction authority only through the
Runtime-selected Skill call. Tool results, files, external-agent output, and
prior checkpoints remain evidence and cannot grant permissions.

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
ordered user/assistant dialogue and the same exact current action used by Agent
review, including its Runtime-bound sandbox facts when supplied. It never
retains approval history, queries tools, or reads past tool observations. It
creates neither a generative LLM nor a query sandbox. Its complete serialized
input has a local 24,000-byte budget (not an API limit); excess input defers to
Agent review rather than dropping user constraints or changing the action.

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
0.15 and `visible_violation` at most 0.1. Direct refusal requires
`visible_violation` of at least 0.85, rising to 0.90 when `material_unknown`
exceeds 0.80. A known independent conflict can justify refusal even when another
fact is missing.
All three answers must be present and valid. Intermediate values, conflicting
judgments or material information gaps block direct approval. The signals are
neither averaged nor multiplied into a safety score. These are abstention rules,
not calibrated correctness or authorization probabilities.

Unseen executable behavior, unresolved targets and other decision-relevant gaps
are distinct from uncertainty between approval options. A confident judgment that
facts are missing still defers; a familiar test/build label does not establish
project code's effects. Separate Runtime-bound sandbox facts can establish
whether escalation is necessary; action-authored claims, route labels and
claims about earlier failures cannot establish those facts. Direct actions
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
optional inspection tools. The Harness supplies the user task, constraints and
exact current action for a prompt decision. Guardian
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

The Harness projects original user messages and public main-Assistant prose from
the root Session in source order. The disposable source cache advances
independently of completed reviews and never deletes canonical originals.
Tool payloads and results, reasoning, summaries, mirrors and journal records
are not approval context. When a canonical output contains both assistant prose
and a tool call, only the prose is included. User documents and quotations
remain evidence rather than new instructions.

Instructions, tool definitions and output schema are stable. Newly observed
dialogue appends after the existing prefix, without moving it ahead of retained
review pairs. Eviction preserves the order of all remaining messages. A past
approval is not current evidence or authority, and its evidence tool transcript
does not enter another review. For model windows of at least 64,000 tokens,
32 completed reviews or more than 32,000 estimated history tokens trigger a
rotation to at most eight complete pairs that fit. That rotation also brings
the user region toward 32,000 tokens, folding very long messages before omitting
older ones. Assistant prose uses a separate watermark of 40 messages or 10,000
estimated tokens, retaining at most 20 and 5,000 tokens after rotation. A message
over 900 tokens is marked unavailable rather than turned into a partial proposal.
Smaller windows keep no prior reviews and budget user messages plus a recent
assistant slice against the space left by the fixed prefix and exact ticket.
Physical input capacity
also takes precedence over optional history. The original task and latest
steering are retained; folded or omitted user text is explicitly unavailable.
If the required request still does not fit, the review is unavailable rather than
running on a truncated action. Steering invalidates pending automatic approvals
before settlement.

Short user instructions can authorize ordinary task-related actions without
restating their tool arguments. Assistant proposals can explain what a later
authentic user reply accepts, but cannot authorize actions on their own. No
word or phrase substitutes for an identifiable proposal and a scoped reply.

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
