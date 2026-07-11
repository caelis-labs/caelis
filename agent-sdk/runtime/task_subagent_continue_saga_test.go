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
	continueCalls atomic.Int32
	continueErr   error
	result        delegation.Result
}

func (r *continueSagaRunner) Spawn(_ context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return delegation.Anchor{TaskID: spawn.TaskID, SessionID: "child-continue", Agent: req.Agent, AgentID: "child-agent-continue"},
		delegation.Result{TaskID: spawn.TaskID, State: delegation.StateCompleted, Result: "spawned"}, nil
}
func (r *continueSagaRunner) Continue(_ context.Context, _ delegation.Anchor, req delegation.ContinueRequest) (delegation.Result, error) {
	r.continueCalls.Add(1)
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

	base := memory.NewService(memory.NewStore(memory.Config{}))
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

	base := memory.NewService(memory.NewStore(memory.Config{}))
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

func TestSubagentContinueSagaRecoversPreparedWithoutRemoteUntilClaim(t *testing.T) {
	t.Parallel()

	base := memory.NewService(memory.NewStore(memory.Config{}))
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
