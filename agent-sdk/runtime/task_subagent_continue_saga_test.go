package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	memory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

type continueSagaRunner struct {
	continueCalls   atomic.Int32
	continueYieldMS atomic.Int32
	waitCalls       atomic.Int32
	continueErr     error
	result          delegation.Result
	continueStarted chan struct{}
	continueRelease chan struct{}
}

func (r *continueSagaRunner) Spawn(_ context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return delegation.Anchor{TaskID: spawn.TaskID, SessionID: "child-continue", Agent: req.Agent, AgentID: "child-agent-continue"},
		delegation.Result{TaskID: spawn.TaskID, State: delegation.StateCompleted, Result: "spawned"}, nil
}
func (r *continueSagaRunner) Continue(_ context.Context, _ delegation.Anchor, req delegation.ContinueRequest) (delegation.Result, error) {
	r.continueCalls.Add(1)
	r.continueYieldMS.Store(int32(req.YieldTimeMS))
	if r.continueStarted != nil {
		select {
		case r.continueStarted <- struct{}{}:
		default:
		}
	}
	if r.continueRelease != nil {
		<-r.continueRelease
	}
	if r.continueErr != nil {
		return delegation.Result{}, r.continueErr
	}
	result := r.result
	if result.State == "" {
		result = delegation.Result{State: delegation.StateCompleted, Result: "continued:" + strings.TrimSpace(req.Prompt)}
	}
	switch result.State {
	case delegation.StateCompleted, delegation.StateFailed, delegation.StateCancelled, delegation.StateInterrupted:
		if req.Completion != nil {
			req.Completion.PublishSubagentCompletion(result)
		}
	}
	return result, nil
}
func (r *continueSagaRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	r.waitCalls.Add(1)
	return delegation.Result{}, nil
}
func (*continueSagaRunner) Cancel(context.Context, delegation.Anchor) error { return nil }

type continueFailFinalSessions struct {
	session.Service
	failFinal   bool
	failedOnce  bool
	finalCalls  int
	appendCalls int
}

func (s *continueFailFinalSessions) PutParticipantWithEvent(ctx context.Context, req session.PutParticipantWithEventRequest) (session.Session, *session.Event, error) {
	return s.Service.(session.ParticipantLifecycleService).PutParticipantWithEvent(ctx, req)
}
func (s *continueFailFinalSessions) RemoveParticipantWithEvent(ctx context.Context, req session.RemoveParticipantWithEventRequest) (session.Session, *session.Event, error) {
	return s.Service.(session.ParticipantLifecycleService).RemoveParticipantWithEvent(ctx, req)
}
func (s *continueFailFinalSessions) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	s.appendCalls++
	if req.Event != nil && req.Event.Type == session.EventTypeAssistant {
		s.finalCalls++
		// Only inject on continuation turns (turn seq >= 2) so spawn's first final still commits.
		if s.failFinal && !s.failedOnce && strings.Contains(req.Event.IdempotencyKey, ":2:assistant") {
			s.failedOnce = true
			return nil, errors.New("forced parent final dual-write failure")
		}
	}
	return s.Service.AppendEvent(ctx, req)
}

func TestSubagentContinueCompletionRetriesFinalWithoutReissuingRemote(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "continue-saga"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := &continueFailFinalSessions{Service: base, failFinal: true}
	store := newSagaTaskStore()
	runner := &continueSagaRunner{}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "continue-saga", Agent: "helper", Prompt: "first", Role: session.ParticipantRoleSidecar, Source: "user",
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up", Principal: session.ActorKindUser, Source: "user",
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if snapshot.Running || snapshot.State != taskapi.StateCompleted ||
		taskStringValue(snapshot.Metadata["continue_phase"]) != "" {
		t.Fatalf("Write() = %#v, want producer terminal snapshot with cleared continue phase", snapshot)
	}
	if got := runner.continueCalls.Load(); got != 1 {
		t.Fatalf("Continue calls after failure = %d, want 1", got)
	}
	entry, err := store.Get(context.Background(), started.Ref.TaskID)
	if err != nil || taskStringValue(entry.Metadata["continue_phase"]) != "" ||
		entry.State != taskapi.StateCompleted || entry.Running {
		t.Fatalf("durable completion = %#v, %v; want terminal with atomically cleared continue phase", entry, err)
	}
	if got := runner.continueCalls.Load(); got != 1 {
		t.Fatalf("Continue calls after completion retry = %d, want 1 (no remote re-issue)", got)
	}
	if sessions.finalCalls < 2 {
		t.Fatalf("final append attempts = %d, want at least 2 (fail then succeed)", sessions.finalCalls)
	}
}

func TestSubagentContinueQueuedCompletionSkipsPostEffectWrite(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "continue-skip-post-effect",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &continueSagaRunner{}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store,
	}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "continue-skip-post-effect", Agent: "helper", Prompt: "first",
		Role: session.ParticipantRoleSidecar, Source: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.failContinuePhase = string(continuePhasePostEffect)
	store.mu.Unlock()

	snapshot, err := runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up", Yield: 2 * time.Second,
		Principal: session.ActorKindUser, Source: "user",
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if snapshot.State != taskapi.StateCompleted || snapshot.Running ||
		taskStringValue(snapshot.Metadata["continue_phase"]) != "" {
		t.Fatalf("Write() = %#v, want terminal producer truth", snapshot)
	}
	if got := runner.continueCalls.Load(); got != 1 {
		t.Fatalf("Continue calls = %d, want one", got)
	}
	if got := runner.continueYieldMS.Load(); got != 0 {
		t.Fatalf("Continue yield = %dms, want issue-only zero yield", got)
	}
	store.mu.Lock()
	postEffectAttempts := store.postEffectAttempts
	failTriggered := store.failedState
	store.mu.Unlock()
	if postEffectAttempts != 0 || failTriggered {
		t.Fatalf("post_effect attempts=%d failure_triggered=%v, want queued completion to skip post_effect CAS", postEffectAttempts, failTriggered)
	}
}

func TestSubagentContinueQueuedCompletionWinsBeforePendingRecovery(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "continue-completion-before-recovery",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &continueSagaRunner{result: delegation.Result{
		State: delegation.StateRunning, Running: true, OutputPreview: "continuing",
	}}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store,
	}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "continue-completion-before-recovery", Agent: "helper", Prompt: "first",
		Role: session.ParticipantRoleSidecar, Source: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.failContinuePhase = string(continuePhasePostEffect)
	store.mu.Unlock()

	pending, err := runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up",
		Principal: session.ActorKindUser, Source: "user",
	})
	if err != nil {
		t.Fatalf("Write(first) error = %v", err)
	}
	if !pending.Running || pending.State != taskapi.StateRunning ||
		taskStringValue(pending.Metadata["continue_phase"]) != string(continuePhasePending) {
		t.Fatalf("Write(first) = %#v, want durable pending recovery fence", pending)
	}

	runtime.tasks.mu.Lock()
	runtime.tasks.enqueueSubagentCompletionLocked(started.Ref.TaskID, pendingSubagentCompletion{
		ctx: context.Background(),
		result: delegation.Result{
			TaskID: started.Ref.TaskID, State: delegation.StateCompleted, Result: "producer terminal",
		},
		turnSeq: 2,
	})
	runtime.tasks.mu.Unlock()

	settled, err := runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up",
		Principal: session.ActorKindUser, Source: "user",
	})
	if err != nil {
		t.Fatalf("Write(recovery) error = %v", err)
	}
	if settled.Running || settled.State != taskapi.StateCompleted ||
		taskStringValue(settled.Result["final_message"]) != "producer terminal" ||
		taskStringValue(settled.Metadata["continue_phase"]) != "" {
		t.Fatalf("Write(recovery) = %#v, want queued producer terminal", settled)
	}
	if got := runner.continueCalls.Load(); got != 1 {
		t.Fatalf("Continue calls = %d, want one without blind re-issue", got)
	}
	entry, err := store.Get(context.Background(), started.Ref.TaskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if entry.Running || entry.State != taskapi.StateCompleted ||
		taskStringValue(runtime.tasks.rehydrateSubagentTask(entry).snapshot().Result["final_message"]) != "producer terminal" {
		t.Fatalf("durable completion = %#v, want producer terminal instead of unknown outcome", entry)
	}
}

func TestSubagentContinueCompletionResolvesRecoveredUnknownOutcome(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "continue-completion-after-recovery",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &continueSagaRunner{result: delegation.Result{
		State: delegation.StateRunning, Running: true, OutputPreview: "continuing",
	}}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store,
	}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "continue-completion-after-recovery", Agent: "helper", Prompt: "first",
		Role: session.ParticipantRoleSidecar, Source: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.failContinuePhase = string(continuePhasePostEffect)
	store.mu.Unlock()

	pending, err := runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up",
		Principal: session.ActorKindUser, Source: "user",
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if !pending.Running || taskStringValue(pending.Metadata["continue_phase"]) != string(continuePhasePending) {
		t.Fatalf("Write() = %#v, want pending continuation", pending)
	}
	release, claimed := runtime.tasks.tryClaimSubagentOperation(active.SessionRef, started.Ref.TaskID)
	if !claimed {
		t.Fatal("tryClaimSubagentOperation() = false")
	}
	task, err := runtime.tasks.lookupSubagentCanonical(context.Background(), active.SessionRef, started.Ref.TaskID)
	if err != nil {
		release()
		t.Fatalf("lookupSubagentCanonical() error = %v", err)
	}
	if err := runtime.tasks.recoverPendingSubagentControlClaimed(context.Background(), task); err != nil {
		release()
		t.Fatalf("recoverPendingSubagentControlClaimed() error = %v", err)
	}
	release()
	recovered := task.snapshot()
	if recovered.Running || recovered.State != taskapi.StateUnknownOutcome ||
		taskStringValue(recovered.Metadata["continue_phase"]) != string(continuePhaseUnknownOutcome) {
		t.Fatalf("recovered = %#v, want provisional unknown outcome", recovered)
	}
	task.mu.Lock()
	if task.streamChanged == nil {
		task.streamChanged = make(chan struct{})
	}
	streamChanged := task.streamChanged
	task.mu.Unlock()
	store.mu.Lock()
	store.failOnPut = store.puts + 1
	store.mu.Unlock()

	newSubagentCompletionSink(
		context.Background(), runtime.tasks, started.Ref.TaskID, 2,
	).PublishSubagentCompletion(delegation.Result{
		TaskID: started.Ref.TaskID, State: delegation.StateCompleted, Result: "late producer terminal",
	})
	live := task.snapshot()
	if live.Running || live.State != taskapi.StateCompleted ||
		taskStringValue(live.Result["final_message"]) != "late producer terminal" ||
		taskStringValue(live.Metadata["continue_phase"]) != "" {
		t.Fatalf("live completion = %#v, want real producer terminal to replace provisional unknown", live)
	}
	select {
	case <-streamChanged:
	default:
		t.Fatal("late completion did not notify the live Task stream")
	}
	settled, err := runtime.tasks.Read(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindUser, Source: "user",
	})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if settled.Running || settled.State != taskapi.StateCompleted ||
		taskStringValue(settled.Result["final_message"]) != "late producer terminal" ||
		taskStringValue(settled.Metadata["continue_phase"]) != "" {
		t.Fatalf("late completion = %#v, want real producer terminal to replace provisional unknown", settled)
	}
	if got := runner.continueCalls.Load(); got != 1 {
		t.Fatalf("Continue calls = %d, want one without remote re-issue", got)
	}
}

func TestSubagentContinueSagaRefusesBlindReissueAfterExternalClaim(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "continue-pending"})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &continueSagaRunner{continueErr: errors.New("forced remote continue failure")}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "continue-pending", Agent: "helper", Prompt: "first", Role: session.ParticipantRoleSidecar, Source: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up", Principal: session.ActorKindUser, Source: "user",
	})
	if err == nil {
		t.Fatal("Write() error = nil, want remote continue failure")
	}
	entry, err := store.Get(context.Background(), started.Ref.TaskID)
	if err != nil || taskStringValue(entry.Metadata["continue_phase"]) != string(continuePhaseUnknownOutcome) {
		t.Fatalf("durable continue phase = %#v, %v; want unknown_outcome", entry, err)
	}
	if entry.State != taskapi.StateUnknownOutcome || entry.Running || entry.SupportsInput {
		t.Fatalf("durable continue state = %#v, want terminal unknown without input", entry)
	}
	if got := taskStringValue(entry.Result["error"]); got != subagentContinueUnknownDiagnostic {
		t.Fatalf("durable continue error = %q, want fixed diagnostic", got)
	}
	for _, key := range []string{"result", "final_message", "output_preview"} {
		if _, exists := entry.Result[key]; exists {
			t.Fatalf("unknown continue retained %q: %#v", key, entry.Result)
		}
	}
	if persisted := strings.Join([]string{taskStringValue(entry.Result["error"]), taskStringValue(entry.Metadata["continue_reason"])}, "\n"); strings.Contains(persisted, "forced remote continue failure") {
		t.Fatalf("unknown continue persisted raw runner error: %q", persisted)
	}
	payload := taskToolPayload(snapshotFromTaskEntry(entry))
	if _, exists := payload["final_message"]; exists {
		t.Fatalf("unknown continue manufactured final message: %#v", payload)
	}

	runner.continueErr = nil
	_, err = runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up", Principal: session.ActorKindUser, Source: "user",
	})
	if err == nil || !strings.Contains(err.Error(), "refusing blind re-issue") {
		t.Fatalf("retry error = %v, want blind re-issue refusal", err)
	}
	if got := runner.continueCalls.Load(); got != 1 {
		t.Fatalf("Continue calls = %d, want 1", got)
	}
}

func TestSubagentContinueRejectsConcurrentOperationBeforeSecondRemoteEffect(t *testing.T) {
	t.Parallel()
	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "continue-concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &continueSagaRunner{continueStarted: make(chan struct{}, 1), continueRelease: make(chan struct{})}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "continue-concurrent", Agent: "helper", Prompt: "first", Role: session.ParticipantRoleSidecar, Source: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, writeErr := runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
			TaskID: started.Ref.TaskID, Input: "follow up", Principal: session.ActorKindUser,
		})
		firstDone <- writeErr
	}()
	<-runner.continueStarted
	if err := runtime.recoverRuntimeState(context.Background(), active.SessionRef); err != nil {
		t.Fatalf("recovery during active Continue error = %v", err)
	}
	if observed, waitErr := runtime.tasks.Wait(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindUser,
	}); waitErr != nil || observed.State != taskapi.StateRunning || !observed.Running {
		t.Fatalf("Wait during Continue = %#v, %v; want non-mutating running observation", observed, waitErr)
	}
	_, secondErr := runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up", Principal: session.ActorKindUser,
	})
	if secondErr == nil || !strings.Contains(secondErr.Error(), "operation in progress") {
		t.Fatalf("concurrent Write error = %v, want operation conflict", secondErr)
	}
	close(runner.continueRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Write error = %v", err)
	}
	if got := runner.continueCalls.Load(); got != 1 {
		t.Fatalf("remote Continue calls = %d, want 1", got)
	}
	if runtime.tasks.hasSubagentOperation(active.SessionRef, started.Ref.TaskID) {
		t.Fatal("completed Continue leaked its operation claim")
	}
}

func TestTaskOperationClaimIsSessionScoped(t *testing.T) {
	t.Parallel()
	runtime := &taskRuntime{operations: map[string]struct{}{}}
	releaseA, claimedA := runtime.tryClaimSubagentOperation(session.SessionRef{SessionID: "session-a"}, "shared-task")
	if !claimedA {
		t.Fatal("first session claim failed")
	}
	defer releaseA()
	releaseB, claimedB := runtime.tryClaimSubagentOperation(session.SessionRef{SessionID: "session-b"}, "shared-task")
	if !claimedB {
		t.Fatal("same task id in a different session falsely conflicted")
	}
	releaseB()
}

func TestSubagentControlReloadsNewerDurableRevisionBeforeDispatch(t *testing.T) {
	t.Parallel()
	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "canonical-reload"})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &continueSagaRunner{}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "canonical-reload", Agent: "helper", Prompt: "first", Role: session.ParticipantRoleSidecar, Source: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.Get(context.Background(), started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	newer := taskapi.CloneEntry(entry)
	newer.State = taskapi.StateUnknownOutcome
	newer.Running = false
	newer.SupportsInput = false
	newer.Metadata["continue_phase"] = string(continuePhaseUnknownOutcome)
	persisted, err := store.Put(context.Background(), taskapi.PutRequest{Entry: newer, ExpectedRevision: entry.Revision})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.tasks.Wait(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != persisted.Revision || snapshot.State != taskapi.StateUnknownOutcome || snapshot.Running {
		t.Fatalf("Wait snapshot = %#v, want reloaded durable revision %d", snapshot, persisted.Revision)
	}
}

func TestSubagentWaitDoesNotRecoverPendingContinue(t *testing.T) {
	t.Parallel()
	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "continue-wait-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &continueSagaRunner{}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "continue-wait-recovery", Agent: "helper", Prompt: "first", Role: session.ParticipantRoleSidecar, Source: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	subagent, err := runtime.tasks.lookupSubagent(context.Background(), active.SessionRef, started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	subagent.beginContinuationTurn()
	digest, err := continueRequestDigest("follow up", agent.ContextTransfer{}, subagent.turnSeq)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.tasks.markSubagentContinuePhase(context.Background(), subagent, continuePhasePending, "follow up", agent.ContextTransfer{}, digest, subagent.turnSeq, ""); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Get(context.Background(), started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := restarted.tasks.Wait(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != taskapi.StateRunning || !snapshot.Running ||
		snapshot.Revision != before.Revision ||
		taskStringValue(snapshot.Metadata["continue_phase"]) != string(continuePhasePending) {
		t.Fatalf("Wait snapshot = %#v, want unchanged durable running/pending revision %d", snapshot, before.Revision)
	}
	after, err := store.Get(context.Background(), started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || runner.waitCalls.Load() != 0 {
		t.Fatalf("Wait mutated revision %d -> %d or polled runner %d times", before.Revision, after.Revision, runner.waitCalls.Load())
	}
}

func TestSubagentContinuePendingPersistenceFailureDoesNotAdvanceLocalPhase(t *testing.T) {
	t.Parallel()
	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "continue-pending-put-fail"})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &continueSagaRunner{}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "continue-pending-put-fail", Agent: "helper", Prompt: "first", Role: session.ParticipantRoleSidecar, Source: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.failOnPut = store.puts + 2 // prepared succeeds; pending claim fails.
	store.mu.Unlock()
	snapshot, err := runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up", Principal: session.ActorKindUser,
	})
	if err == nil {
		t.Fatal("Write() error = nil, want pending persistence failure")
	}
	if got := runner.continueCalls.Load(); got != 0 {
		t.Fatalf("Continue calls = %d, want no remote effect", got)
	}
	if got := taskStringValue(snapshot.Metadata["continue_phase"]); got != string(continuePhasePrepared) {
		t.Fatalf("local phase = %q, want prepared", got)
	}
	entry, getErr := store.Get(context.Background(), started.Ref.TaskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got := taskStringValue(entry.Metadata["continue_phase"]); got != string(continuePhasePrepared) {
		t.Fatalf("durable phase = %q, want prepared", got)
	}
}

func TestSubagentContinueUnknownPersistenceFailureLeavesLocalAndDurablePending(t *testing.T) {
	t.Parallel()
	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "continue-unknown-put-fail"})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &continueSagaRunner{continueErr: errors.New("forced remote continue failure")}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "continue-unknown-put-fail", Agent: "helper", Prompt: "first", Role: session.ParticipantRoleSidecar, Source: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.failOnPut = store.puts + 3 // prepared + pending succeed; unknown fails.
	store.mu.Unlock()
	snapshot, err := runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up", Principal: session.ActorKindUser,
	})
	if err == nil {
		t.Fatal("Write() error = nil, want remote and unknown persistence failures")
	}
	if got := runner.continueCalls.Load(); got != 1 {
		t.Fatalf("Continue calls = %d, want one remote attempt", got)
	}
	if got := taskStringValue(snapshot.Metadata["continue_phase"]); got != string(continuePhasePending) ||
		snapshot.State != taskapi.StateRunning || !snapshot.Running {
		t.Fatalf("local snapshot = %#v, want running/pending retained", snapshot)
	}
	entry, getErr := store.Get(context.Background(), started.Ref.TaskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got := taskStringValue(entry.Metadata["continue_phase"]); got != string(continuePhasePending) ||
		entry.State != taskapi.StateRunning || !entry.Running || entry.SupportsInput {
		t.Fatalf("durable entry = %#v, want running/pending retained", entry)
	}
}

func TestRecoverRuntimeStatePromotesPendingContinueToDurableUnknown(t *testing.T) {
	t.Parallel()
	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "continue-recover-pending"})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	entry := &taskapi.Entry{
		TaskID: "pending-task", Kind: taskapi.KindSubagent, Session: active.SessionRef,
		State: taskapi.StateCompleted, Running: false, SupportsInput: true,
		Spec:     map[string]any{"continue_phase": string(continuePhasePending)},
		Metadata: map[string]any{"continue_phase": string(continuePhasePending)},
		Result: map[string]any{
			"state":          string(taskapi.StateCompleted),
			"result":         "previous completed turn",
			"final_message":  "previous completed turn",
			"output_preview": "stale activity",
		},
	}
	if _, err := store.Put(context.Background(), taskapi.PutRequest{Entry: entry}); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: &continueSagaRunner{}, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.recoverRuntimeState(context.Background(), active.SessionRef); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), entry.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != taskapi.StateUnknownOutcome || got.Running || got.SupportsInput ||
		taskStringValue(got.Metadata["continue_phase"]) != string(continuePhaseUnknownOutcome) {
		t.Fatalf("recovered task = %#v, want durable unknown outcome", got)
	}
	if gotDiagnostic := taskStringValue(got.Result["error"]); gotDiagnostic != subagentContinueUnknownDiagnostic {
		t.Fatalf("recovered error = %q, want fixed diagnostic", gotDiagnostic)
	}
	for _, key := range []string{"result", "final_message", "output_preview"} {
		if _, exists := got.Result[key]; exists {
			t.Fatalf("recovered unknown outcome retained %q: %#v", key, got.Result)
		}
	}
	rehydrated := runtime.tasks.rehydrateSubagentTask(got).snapshot()
	payload := taskToolPayload(rehydrated)
	if taskStringValue(payload["error"]) != subagentContinueUnknownDiagnostic {
		t.Fatalf("rehydrated Task payload = %#v, want fixed diagnostic", payload)
	}
	if _, exists := payload["final_message"]; exists {
		t.Fatalf("rehydrated unknown outcome manufactured final message: %#v", payload)
	}
}

func TestSubagentContinueSagaRecoversPreparedWithoutRemoteUntilClaim(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "continue-prepared"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := &continueFailFinalSessions{Service: base}
	// Fail the first user append by wrapping: use fail on first canonical user after prepared.
	// Simpler: seed prepared phase and resume.
	store := newSagaTaskStore()
	runner := &continueSagaRunner{}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "continue-prepared", Agent: "helper", Prompt: "first", Role: session.ParticipantRoleSidecar, Source: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Drive a real continue through prepared by failing put on continue_pending claim.
	store.failStatus = string(continuePhasePending)
	// failStatus checks spawn_status in saga store — extend for continue_phase.
	// Use failOnPut after spawn is done: count puts after start.
	// Easier path: mark prepared manually then resume.
	task, err := runtime.tasks.lookupSubagent(context.Background(), active.SessionRef, started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := task.beginContinuationTurn()
	digest, err := continueRequestDigest("follow up", agent.ContextTransfer{}, task.turnSeq)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.tasks.markSubagentContinuePhase(context.Background(), task, continuePhasePrepared, "follow up", agent.ContextTransfer{}, digest, task.turnSeq, ""); err != nil {
		t.Fatal(err)
	}
	_ = checkpoint
	snapshot, err := runtime.tasks.continueSubagent(context.Background(), task, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up", Principal: session.ActorKindUser, Source: "user",
	})
	if err != nil {
		t.Fatalf("prepared resume error = %v", err)
	}
	if got := runner.continueCalls.Load(); got != 1 {
		t.Fatalf("Continue calls = %d, want 1", got)
	}
	if taskStringValue(snapshot.Metadata["continue_phase"]) != "" ||
		snapshot.Running || snapshot.State != taskapi.StateCompleted {
		t.Fatalf("continue snapshot = %#v, want producer terminal with cleared continue phase", snapshot)
	}
	entry, err := store.Get(context.Background(), started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if taskStringValue(entry.Metadata["continue_phase"]) != "" ||
		entry.Running || entry.State != taskapi.StateCompleted {
		t.Fatalf("durable completion = %#v, want terminal with cleared continue phase", entry)
	}
}
