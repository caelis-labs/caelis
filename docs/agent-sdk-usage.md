# Agent SDK Usage and Compatibility

The Caelis Agent SDK is a package tree in the root
`github.com/caelis-labs/caelis` module. It is versioned with Caelis rather than
as a separate Go module. This guide defines the host-facing behavior that SDK
consumers may rely on.

## Requirements and support scope

- Minimum Go version: **Go 1.25.1**, matching the root `go.mod`. A Caelis
  release may raise this only as an explicit compatibility change.
- Supported build targets: macOS, Linux, and Windows on amd64 and arm64, which
  are the six targets produced by the Caelis release workflow.
- The primary quality workflow runs on Linux. Platform-specific sandbox tests
  run only where their operating-system APIs are available; consumers should
  test their selected backend on every deployment platform.
- Only imports listed in
  [`agent-sdk/supported-packages.txt`](../agent-sdk/supported-packages.txt) are
  supported SDK API before v1. Bundled packages such as `runtime/chat`,
  `session/memory`, `session/file`, provider implementations, builtin tools,
  and concrete sandbox backends ship with the same Caelis release but may
  evolve before v1.

Use a Caelis module tag in a consumer module:

```bash
go get github.com/caelis-labs/caelis@<version>
```

## Quickstart

The following offline example uses only supported imports. It supplies a
host-local Agent and immutable invocation context, so it does not make a source
compatibility promise for bundled model providers, stores, or Agent factories.
The checked-in
[`Example`](../agent-sdk/runtime/quickstart_external_test.go) is compiled and
executed by `go test ./agent-sdk/runtime`; CI also parses its imports against the
17-package allowlist.

```go
package main

import (
	"context"
	"fmt"
	"iter"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type greetingAgent struct{}

func (greetingAgent) Name() string { return "greeting" }
func (greetingAgent) Run(ctx agent.Context) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant,
			fmt.Sprintf("received:%d", ctx.Events().Len()))
		yield(&session.Event{Type: session.EventTypeAssistant, Message: &message}, nil)
	}
}

func main() {
	user := model.NewTextMessage(model.RoleUser, "Say hello.")
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{
			AppName: "quickstart", UserID: "local-user", SessionID: "hello",
		}},
		Events: []*session.Event{{Type: session.EventTypeUser, Message: &user}},
	})
	for event, err := range (greetingAgent{}).Run(ctx) {
		if err != nil { panic(err) }
		fmt.Println(session.EventText(event))
	}
}
```

Production Runtime hosts still declare every capability they require in
`AgentSpec` and on the actual model, tools, and sandbox executor. Runtime
validates model/output requirements defensively; the Caelis Control host also
derives and validates the final assembled model/tool/sandbox requirements.

## Concurrency contract

- One Runtime instance permits one active run per normalized
  `session.SessionRef`. A competing run on that instance returns
  `*agent.RunConflictError` with code `errorcode.Conflict`. This in-memory guard
  is not a cross-process lease. Control must establish one owner with
  `SessionLeaseService` or an equivalent CAS policy. The production Gateway
  wraps its execution Runtime with the Control-owned leased Runtime, which
  acquires before dispatch, heartbeats for the asynchronous Runner lifetime,
  cancels on heartbeat failure, and releases on completion/close.
- A `Runner` has one bounded, single-consumer event stream. Select `Events` or
  the optional source-event view once. A second consumer receives
  `runtime.ErrEventStreamConsumed`; fan-out belongs in the host after one
  consumer reads the stream.
- A task stream subscription or stream `Wait` resolves its task once and keeps
  that task generation until close. It cannot switch to a reconstructed task
  during the terminal-result persistence window. Point-in-time stream `Read`
  resolves current durable state on each call.
- Event publication applies backpressure when the bounded queue is full. A
  slow consumer therefore bounds memory at the cost of slowing the run.
- Session mutations use revision compare-and-swap. Concurrent writers pass
  `ExpectedRevision`; a stale writer receives `session.ErrRevisionConflict`
  with code `errorcode.Conflict` and must reload before retrying.
- Every lease acquisition has a distinct `LeaseID` even when `OwnerID` is the
  same. Stores persist a monotonic `FencingToken` per session; heartbeats change
  only the lease revision, while release, expiry, and takeover never permit an
  older token to become current again.
- The lease serializes one canonical Turn, not one Agent identity. Local main
  turns, ACP-controlled main turns, and Side ACP/Reviewer participant prompts
  all acquire it and keep it through the asynchronous Runner lifetime. ACP
  event forwarders receive and preserve the same `MutationGuard` as Runtime;
  dropping it is a lease conflict rather than an unfenced fallback.
- Runtime-owned event, batch, compound, controller, task, and participant-prompt
  mutations carry `MutationAuthorityRuntime` plus the active fence. Memory and
  file stores validate it in the same atomic write section and return
  `session.ErrLeaseConflict` for an expired or replaced owner.
- Non-Run Control writes opt in with a named `session.ControlMutationGuard`.
  Approval resolution, participant attach/detach, watchdog audit, validated
  system commits, and tests may overlap a live Turn. Participant lifecycle
  remains protected by revision/delegation/generation CAS and atomic event
  persistence. Session lifecycle and configuration writes require a quiescent
  Session. Unknown purposes fail closed; handoff and coordinator binding always
  require the matching execution fence, including while the Session is
  quiescent.
- Exclusive Control mutations use
  `session.ControlMutationGuardWithRuntimeLease`. Controller handoff first
  acquires the Session execution lease, starts no endpoint when an old Turn is
  active, and commits the binding plus handoff event only under the matching
  fence. Losing the lease invalidates the commit.
- An unscoped event/binding mutation cannot silently bypass a live lease.
- A Runtime lease context is scoped to exactly one Session. A nested Runtime
  that operates on a distinct staging Session first uses
  `session.ContextWithoutRuntimeLease`, then establishes its own placement
  lease if that store is shared. The helper preserves cancellation, deadlines,
  and unrelated context values; it cannot bypass an active store lease.
- Store adapters implement that CAS contract. Checkpoint compaction also carries
  the source session revision and abandons stale work on conflict.
- Task records and optional session leases also use revision/owner tokens.
  Control owns placement, lease renewal policy, and retry policy.

## Cancellation and close contract

- Cancelling the parent context or calling `Runner.Cancel` requests run
  cancellation. `CancelResult.Status` distinguishes a newly accepted request
  from an already-cancelled or already-finished run; `CancelResult.Err`
  reports adapter termination failures.
- For journaled tool work, `cancel_requested` is durable state distinct from
  terminal `cancelled`. A request is not proof that the external side effect
  stopped. Non-idempotent work interrupted after dispatch may recover as
  `unknown_outcome` and must not be replayed blindly.
- `Runner.Close` on unfinished work cancels it, discards queued events, and
  unblocks producers and consumers. Natural completion closes production but
  preserves queued events so the selected consumer can drain them. Callers
  that stop iteration early must call `Close`.
- The Control watchdog may Interrupt a live Turn only for high-confidence
  generation-loop evidence. Its reviewer is asynchronous and Runtime-wide
  bounded; capacity saturation drops evidence. Reviewer timeout/failure/panic,
  checkpoint failure, and decisions arriving after normal completion never
  become Turn errors or capacity-triggered cancellation.
- Approval resolution is persisted before a waiting run is awakened.
  `AttachLiveRun(runID)` attaches only to execution still registered in the
  current Runtime process. It is not durable continuation. After restart, a
  durable but non-live run returns `*agent.RunNotAttachableError`; recovery
  records an interrupted state instead of pretending execution resumed.
- A committed-but-reported approval resolution is confirmed with a bounded
  context detached from resolver cancellation. Matching idempotent retries
  redeliver the durable decision; conflicting decisions fail closed.

## Event ordering and replay contract

- Every newly durable event receives the current event schema, a unique ID,
  and a strictly increasing `Seq` within its session. `Session.Revision`
  advances with committed mutations. Stream delivery preserves producer
  order, but only persisted `Seq` is a restart-safe ordering key.
- `VisibilityCanonical` message and tool events are model truth. UI-only and
  overlay events are presentation state and must not be promoted into durable
  model context. Product ACP projections and undocumented `_meta` values are
  not replay sources.
- A tool-call event precedes its matching terminal tool-result event. Durable
  `Run`, `Turn`, `Step`, `PauseToken`, and `ToolExecution` records use validated
  transition and revision rules. A terminal tool result and its journal
  transition are one compound commit in capable stores.
- Local terminal tool results are canonical-truncated before they become model
  or session history. When the runtime can write the pre-truncation text or
  JSON to its private system-temporary cache, the same canonical result seen
  live and on replay carries that absolute path in a model-visible system hint.
  The file is optional and evictable; durable context never depends on it and
  never stores the omitted bytes.
- Recovered tool state derives a minimal canonical payload directly from
  `RecoveryStatus`; only genuinely unknown outcomes carry the no-blind-retry
  instruction, and live/rebuilt model contexts match.
- Compaction checkpoints identify the greatest summarized event `Seq`. Replay
  chooses the valid checkpoint with the highest coverage and then applies
  later canonical events; file order alone does not choose a checkpoint.
- Event, execution-journal, and tool-execution records are schema-versioned.
  Adapters migrate older known versions before validation and reject unknown
  future versions or migration gaps. The checked-in v0/v1 replay corpus must
  rebuild exactly the same whole `[]model.Message` as a live runtime turn.

## Persistence contract

- Implement `session.Service` for the basic lifecycle/read/append/binding/state
  surface. Accept the narrower `session.Lifecycle`, `Reader`, `EventAppender`,
  or state/binding interfaces when that is all a component needs.
- `AppendEventRequest.ExpectedRevision` and the batch equivalents are CAS, not
  advisory hints. An event `ID` or `IdempotencyKey` may be retried only with the
  same semantic payload; an identical retry deduplicates and a changed payload
  conflicts.
- Runtime features that change multiple durable facts require the matching
  atomic capability: `EventBatchService`, `EventBatchStateService`,
  `ParticipantLifecycleService`, `ControllerHandoffService`, or the execution
  journal compound-commit interfaces. An adapter must not expose one of these
  interfaces unless it can prevent readers from observing a split commit.
- `task.CASStore.Put` must honor both task revision CAS and the owning session
  mutation fence carried by its context. A committed reporting error may return
  the exact persisted entry; consumers validate and adopt that revision instead
  of treating an already committed write as absent.
- Participant lifecycle stores treat participant ID plus delegation ID as one
  durable identity. Live ACP endpoints additionally carry an attachment
  generation and are indexed by parent SessionID plus participant ID. Every new
  endpoint client rotates generation across reconnect/restart; compensation and
  detach must conditionally match delegation and generation so a stale
  operation cannot remove a newer endpoint. Attach
  compares delegation even when it is empty, and atomic attach or detach honors
  `ExpectedDelegationID`. Participant prompts are single-flight per live
  attachment; Runtime rejects a second prompt before appending its durable user
  event, so it cannot share turn, event, or approval state with the first.
- Participant stores can run the supported `session/sessiontest` conformance
  matrix. It covers plain and lifecycle put/remove, revision/delegation CAS,
  lease MutationGuard fencing on every mutation shape, atomic event conflicts, and exact committed
  results through a store-supplied post-commit fault adapter. The suite is
  strict: lease fencing and fault injection are required capabilities, and
  returned Session/event objects must exactly match durable state and lifecycle
  semantics.
- With a configured durable task store, asynchronous command start requires
  `task.CASStore`. `(SessionID, ParentCall)` is the stable effect identity;
  intent, effect, cleanup, and cancellation claims are persisted before their
  external sandbox actions. `command_intent` and `command_cancel_claimed` mean
  that their respective external effect is authorized but has not yet reached a
  durable post-attempt phase, so recovery may roll them forward. A retry never
  repeats Start after `command_effect_claimed`, or Terminate after cancellation
  is recorded as unknown/applied. Unconfirmed Wait/Status or terminal output
  retrieval preserves the stable TaskID, durable unknown outcome, and live
  handle.
- The current subagent spawn path requires `task.CASStore` before invoking the
  external effect. Its durable phases are intentionally few: `prepared`
  (intent), `spawning` (external-effect claim), `spawned` (post-spawn local
  roll-forward), and `committed`, plus compensation terminals
  (`compensating` / `child_cancelled` / `compensated` / `unknown_outcome`).
  Restart never respawns across `spawning`. Failures before a durable
  post-spawn record compensate by cancellation and durable detach. From
  `spawned`, attach and canonical dialogue use idempotent facts and only mark
  `committed` once; there are no pure intermediate marker phases. A
  cancellation failure remains `unknown_outcome`.
- Subagent Continue uses the same effect-boundary style on one task entry:
  `continue_prepared` → `continue_pending` → `continue_post_effect` → cleared.
  Parent user intent is durable before the remote claim; after
  `continue_post_effect` recovery only finishes the parent final dual-write
  (idempotent assistant key) and never re-issues the remote Continue. A
  process restart or remote failure after `continue_pending` is
  `continue_unknown_outcome` and refuses blind re-issue.
- Subagent Cancel persists `subagent_cancel_claimed` before invoking the remote
  effect. Once claimed, retries and restarts reconcile through Wait instead of
  blindly invoking Cancel again; ordinary non-committed task-store failures
  evict the process-local task so equal-revision cache cannot outrank durable
  state.
- The bundled file store writes a fsynced transaction marker before applying
  event and state documents and completes recovery before later operations.
  A post-commit reporting failure is a committed/unknown-reporting outcome;
  retry with the same idempotency identity rather than inventing a new event.
- Compound event/state mutations bind `TransactionID` to a caller-declared
  `MutationDigest` plus the canonical event payload digest. An identical retry
  is recognized before stale `ExpectedRevision` CAS is evaluated and does not
  invoke the callback again. Reusing the transaction identity with changed
  event or mutation semantics conflicts. Legacy bool-only transaction records
  remain readable but cannot prove a digest and therefore fail closed on retry.
- Persist semantic model state, not transcript caches. Recursive JSON values
  must be copied on input and output so callers cannot mutate stored state
  without a store operation.

## Errors and compatibility

Use `errorcode.CodeOf(err)` or `errors.As`/`errors.Is` with documented typed
errors. Human-readable `Error()` text is diagnostic and may change; it is not a
control-flow or wire contract. External protocol and OS adapters may inspect a
foreign diagnostic once, but must normalize it into an SDK type or code before
Core makes a semantic decision.

Supported declarations are recorded in [`agent-sdk/api.txt`](../agent-sdk/api.txt)
and checked by `make sdk-boundary-check` from an external consumer module. The
SDK shares the root Caelis release version and dependency graph. Before v1,
changes to bundled imports outside the allowlist do not carry a source-
compatibility promise; durable schema compatibility and the contracts above
still apply to data written by supported reference stores.

The declaration snapshot detects an unreviewed worktree change. The
`sdk-api-compat` gate automatically compares it with the most recent reachable
prior release tag; when running at a candidate tag, that tag itself is skipped.
`agent-sdk/api-compat-waivers.json` may use `auto`, while an explicit operator
override is available for controlled diagnostics. Additions are accepted; a removed or
changed old declaration must match an exact package/SHA-256 waiver with a
specific pre-v1 reason. Stale waivers fail, so they cannot silently authorize a
different future change. This is source-declaration evidence; behavioral
compatibility is covered separately by the supported-consumer quickstart and
proxy smoke tests. The current-source gate compiles the worktree fixture with a
local module replacement. The tagged-artifact gate extracts the target tag's
own fixture and package list, resolves the exact proxy version, and forbids
`replace`.

## Sandbox platforms

The supported `sandbox` contracts are platform-neutral. Concrete backends are:

| Platform | Native backend | Notes |
| --- | --- | --- |
| macOS | Seatbelt | Darwin-only process/file policy backend. |
| Linux | Landlock or bubblewrap | Landlock depends on kernel support; bubblewrap depends on the `bwrap` executable. |
| Windows | Windows restricted process/ACL/job backend | Uses Windows-only APIs and helper lifecycle. |
| All supported targets | Host | Command execution without sandbox isolation; it is not a security substitute for a native backend. |

When backend selection is automatic, `sandbox.New` may expose an explicit host
fallback in its status if no native candidate is available. Hosts must inspect
that status and decide whether their policy allows host execution. Requesting a
specific unavailable backend fails instead of silently selecting another one.
