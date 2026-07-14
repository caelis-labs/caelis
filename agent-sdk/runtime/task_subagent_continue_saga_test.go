package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	memory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

type continueSagaRunner struct {
	continueCalls   atomic.Int32
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
	return result, nil
}
func (*continueSagaRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
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

func TestSubagentContinueSagaRollsForwardFinalWithoutReissuingRemote(t *testing.T) {
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

	_, err = runtime.tasks.Write(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up", Principal: session.ActorKindUser, Source: "user",
	})
	if err == nil {
		t.Fatal("Write() error = nil, want parent final dual-write failure")
	}
	if got := runner.continueCalls.Load(); got != 1 {
		t.Fatalf("Continue calls after failure = %d, want 1", got)
	}
	entry, err := store.Get(context.Background(), started.Ref.TaskID)
	if err != nil || taskStringValue(entry.Metadata["continue_phase"]) != string(continuePhasePostEffect) {
		t.Fatalf("durable continue phase = %#v, %v; want post_effect", entry, err)
	}

	// Restart: rehydrate post_effect and finish parent final without remote re-issue.
	restarted, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	// Reload in-memory task from store via lookup path used by Write.
	rehydrated, err := restarted.tasks.lookupSubagent(context.Background(), active.SessionRef, started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	rehydrated.runner = runner
	snapshot, err := restarted.tasks.continueSubagent(context.Background(), rehydrated, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Input: "follow up", Principal: session.ActorKindUser, Source: "user",
	})
	if err != nil {
		t.Fatalf("continue recovery error = %v", err)
	}
	if got := runner.continueCalls.Load(); got != 1 {
		t.Fatalf("Continue calls after recovery = %d, want 1 (no blind re-issue)", got)
	}
	if taskStringValue(snapshot.Metadata["continue_phase"]) != "" {
		t.Fatalf("continue_phase after recovery = %q, want cleared", taskStringValue(snapshot.Metadata["continue_phase"]))
	}
	entry, err = store.Get(context.Background(), started.Ref.TaskID)
	if err != nil || taskStringValue(entry.Metadata["continue_phase"]) != "" {
		t.Fatalf("durable continue phase after recovery = %#v, %v; want cleared", entry, err)
	}
	if sessions.finalCalls < 2 {
		t.Fatalf("final append attempts = %d, want at least 2 (fail then succeed)", sessions.finalCalls)
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
	if stale, waitErr := runtime.tasks.Wait(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindUser,
	}); waitErr == nil || !strings.Contains(waitErr.Error(), "operation in progress") {
		t.Fatalf("Wait during Continue = %#v, %v; want operation conflict", stale, waitErr)
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
	handle := taskStringValue(started.Metadata["handle"])
	snapshot, err := runtime.tasks.Wait(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: handle, Principal: session.ActorKindUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != persisted.Revision || snapshot.State != taskapi.StateUnknownOutcome || snapshot.Running {
		t.Fatalf("Wait snapshot = %#v, want reloaded durable revision %d", snapshot, persisted.Revision)
	}
}

func TestSubagentWaitRecoversPendingContinueBeforeReturningSnapshot(t *testing.T) {
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
	digest, err := continueRequestDigest("follow up", "", subagent.turnSeq)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.tasks.markSubagentContinuePhase(context.Background(), subagent, continuePhasePending, "follow up", "", digest, subagent.turnSeq, ""); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := restarted.tasks.Wait(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != taskapi.StateUnknownOutcome || snapshot.Running || snapshot.SupportsInput ||
		taskStringValue(snapshot.Metadata["continue_phase"]) != string(continuePhaseUnknownOutcome) {
		t.Fatalf("Wait snapshot = %#v, want recovered unknown outcome", snapshot)
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
	if got := taskStringValue(snapshot.Metadata["continue_phase"]); got != string(continuePhasePending) || snapshot.State != taskapi.StateCompleted {
		t.Fatalf("local snapshot = %#v, want completed/pending retained", snapshot)
	}
	entry, getErr := store.Get(context.Background(), started.Ref.TaskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got := taskStringValue(entry.Metadata["continue_phase"]); got != string(continuePhasePending) || entry.State != taskapi.StateCompleted || !entry.SupportsInput {
		t.Fatalf("durable entry = %#v, want completed/pending retained", entry)
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
	digest, err := continueRequestDigest("follow up", "", task.turnSeq)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.tasks.markSubagentContinuePhase(context.Background(), task, continuePhasePrepared, "follow up", "", digest, task.turnSeq, ""); err != nil {
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
	if taskStringValue(snapshot.Metadata["continue_phase"]) != "" {
		t.Fatalf("continue_phase = %q, want cleared", taskStringValue(snapshot.Metadata["continue_phase"]))
	}
}
