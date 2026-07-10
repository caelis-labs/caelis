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

The following offline example uses the bundled in-memory session service and
chat agent. A production host normally replaces the static model and chooses a
durable session adapter. The checked-in
[`ExampleRuntime_Run`](../agent-sdk/runtime/quickstart_external_test.go) is
compiled and executed by `go test ./agent-sdk/runtime`.

```go
package main

import (
	"context"
	"fmt"
	"iter"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

type staticModel struct{}

func (staticModel) Name() string { return "static" }
func (staticModel) Capabilities() model.Capabilities { return model.Capabilities{} }
func (staticModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(model.StreamEventFromResponse(&model.Response{
			Message:      model.NewTextMessage(model.RoleAssistant, "Hello from Caelis."),
			Status:       model.ResponseStatusCompleted,
			FinishReason: model.FinishReasonStop,
			TurnComplete: true,
		}), nil)
	}
}

func main() {
	ctx := context.Background()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "quickstart", UserID: "local-user",
	})
	if err != nil { panic(err) }

	rt, err := runtime.New(runtime.Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be concise."},
	})
	if err != nil { panic(err) }

	result, err := rt.Run(ctx, agent.RunRequest{
		SessionRef: active.SessionRef,
		Input: "Say hello.",
		AgentSpec: agent.AgentSpec{Name: "assistant", Model: staticModel{}},
	})
	if err != nil { panic(err) }
	defer result.Handle.Close()

	for event, eventErr := range result.Handle.Events() {
		if eventErr != nil { panic(eventErr) }
		if session.EventTypeOf(event) == session.EventTypeAssistant {
			fmt.Println(session.EventText(event))
		}
	}
}
```

Declare every capability the host requires in `AgentSpec` and on the model,
tools, and sandbox executor. Runtime rejects an undeclared requirement before
making the run durable. Set `RunRequest.Limits` to bound model calls, tool
calls, completed turns, wall time, and provider-reported token or cost usage.

## Concurrency contract

- Core permits one active run per normalized `session.SessionRef`. A competing
  run returns `*agent.RunConflictError` with code `errorcode.Conflict`. Host
  Control decides whether to queue the request, reject it, or fork a session;
  Runtime does not make that product-policy decision.
- A `Runner` has one bounded, single-consumer event stream. Select `Events` or
  the optional source-event view once. A second consumer receives
  `runtime.ErrEventStreamConsumed`; fan-out belongs in the host after one
  consumer reads the stream.
- Event publication applies backpressure when the bounded queue is full. A
  slow consumer therefore bounds memory at the cost of slowing the run.
- Session mutations use revision compare-and-swap. Concurrent writers pass
  `ExpectedRevision`; a stale writer receives `session.ErrRevisionConflict`
  with code `errorcode.Conflict` and must reload before retrying.
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
- Approval resolution is persisted before a waiting run is awakened.
  `Resume(runID)` reattaches only a continuation still live in the current
  process. After restart, a durable but non-live run returns
  `*agent.RunNotResumableError`; recovery records an interrupted state instead
  of pretending execution resumed.

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
- The bundled file store writes a fsynced transaction marker before applying
  event and state documents and completes recovery before later operations.
  A post-commit reporting failure is a committed/unknown-reporting outcome;
  retry with the same idempotency identity rather than inventing a new event.
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

