package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

type cancelEffectProbeRunner struct {
	mu          sync.Mutex
	cancelCalls int
	waitCalls   int
	waitResult  delegation.Result
}

type cancelCompletionRaceRunner struct {
	mu          sync.Mutex
	spawn       subagent.SpawnContext
	result      delegation.Result
	cancelCalls int
}

func (r *cancelCompletionRaceRunner) Spawn(_ context.Context, spawn subagent.SpawnContext, _ delegation.Request) (delegation.Anchor, delegation.Result, error) {
	r.mu.Lock()
	r.spawn = spawn
	r.mu.Unlock()
	return delegation.Anchor{
		TaskID: spawn.TaskID, SessionID: "cancel-race-child", AgentID: spawn.TaskID,
	}, delegation.Result{TaskID: spawn.TaskID, State: delegation.StateRunning, Running: true}, nil
}

func (*cancelCompletionRaceRunner) Continue(context.Context, delegation.Anchor, delegation.ContinueRequest) (delegation.Result, error) {
	return delegation.Result{}, errors.New("unexpected continue")
}

func (r *cancelCompletionRaceRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return delegation.CloneResult(r.result), nil
}

func (r *cancelCompletionRaceRunner) Cancel(context.Context, delegation.Anchor) error {
	r.mu.Lock()
	r.cancelCalls++
	sink := r.spawn.Completion
	result := delegation.CloneResult(r.result)
	taskID := r.spawn.TaskID
	r.mu.Unlock()
	if sink != nil {
		result.TaskID = taskID
		sink.PublishSubagentCompletion(result)
	}
	return nil
}

func (*cancelEffectProbeRunner) Spawn(context.Context, subagent.SpawnContext, delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return delegation.Anchor{}, delegation.Result{}, errors.New("unexpected spawn")
}

func (*cancelEffectProbeRunner) Continue(context.Context, delegation.Anchor, delegation.ContinueRequest) (delegation.Result, error) {
	return delegation.Result{}, errors.New("unexpected continue")
}

func (r *cancelEffectProbeRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waitCalls++
	return delegation.CloneResult(r.waitResult), nil
}

func (r *cancelEffectProbeRunner) Cancel(context.Context, delegation.Anchor) error {
	r.mu.Lock()
	r.cancelCalls++
	r.mu.Unlock()
	return nil
}

func (r *cancelEffectProbeRunner) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelCalls
}

func (r *cancelEffectProbeRunner) waits() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waitCalls
}

func TestSubagentCancelDoesNotPollOrRepeatRemoteEffect(t *testing.T) {
	t.Parallel()

	runner := &cancelEffectProbeRunner{waitResult: delegation.Result{State: delegation.StateCancelled, Result: "cancelled"}}
	runtime, active := newSubagentTaskTestRuntime(t, runner)
	store := newSagaTaskStore()
	runtime.tasks.store = store
	entry := &taskapi.Entry{
		TaskID: "cancel-persist-split", Kind: taskapi.KindSubagent, Session: active.SessionRef,
		State: taskapi.StateRunning, Running: true, SupportsCancel: true,
		Spec: map[string]any{
			"agent": "reviewer", "session_id": "child-cancel", "agent_id": "child-agent",
			"handle": "reviewer-1", "spawn_phase": spawnStatusCommitted,
		},
		Result: map[string]any{"state": string(taskapi.StateRunning), "handle": "reviewer-1"},
		Metadata: map[string]any{
			"state": string(taskapi.StateRunning), "running": true, "handle": "reviewer-1",
			"spawn_status": spawnStatusCommitted,
		},
	}
	if _, err := store.Put(context.Background(), taskapi.PutRequest{Entry: entry}); err != nil {
		t.Fatal(err)
	}
	req := taskapi.ControlRequest{TaskID: entry.TaskID, Principal: session.ActorKindController}
	first, err := runtime.tasks.Cancel(context.Background(), active.SessionRef, req)
	if err != nil {
		t.Fatalf("first Cancel() error = %v", err)
	}
	if first.Ref.TaskID != entry.TaskID || runner.calls() != 1 || runner.waits() != 0 ||
		first.State != taskapi.StateUnknownOutcome || !first.Running {
		t.Fatalf("first Cancel() = %#v calls=%d waits=%d; want one remote effect and producer-pending lifecycle", first, runner.calls(), runner.waits())
	}
	second, err := runtime.tasks.Cancel(context.Background(), active.SessionRef, req)
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls() != 1 || runner.waits() != 0 ||
		second.State != taskapi.StateUnknownOutcome || !second.Running {
		t.Fatalf("second Cancel() = %#v calls=%d waits=%d; want durable pending phase without re-Cancel or Wait", second, runner.calls(), runner.waits())
	}
}

func TestSubagentCancelPreservesNaturallyCompletedTerminal(t *testing.T) {
	t.Parallel()

	runner := &cancelCompletionRaceRunner{
		result: delegation.Result{State: delegation.StateCompleted, Result: "natural completion won"},
	}
	runtime, active := newSubagentTaskTestRuntime(t, runner)
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		Agent: "reviewer", Prompt: "review",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	snapshot, err := runtime.tasks.Cancel(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindController,
	})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if snapshot.State != taskapi.StateCompleted || snapshot.Running || !snapshot.SupportsInput ||
		taskStringValue(snapshot.Result["final_message"]) != "natural completion won" {
		t.Fatalf("Cancel() = %#v, want producer terminal snapshot", snapshot)
	}
	observed, err := runtime.tasks.Read(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindController,
	})
	if err != nil {
		t.Fatalf("Read(after Cancel completion handoff) error = %v", err)
	}
	if observed.State != taskapi.StateCompleted || observed.Running || !observed.SupportsInput ||
		taskStringValue(observed.Result["final_message"]) != "natural completion won" {
		t.Fatalf("Read(after Cancel completion handoff) = %#v, want natural completed terminal", observed)
	}
	durable, err := runtime.tasks.store.Get(context.Background(), started.Ref.TaskID)
	if err != nil {
		t.Fatalf("TaskStore.Get() error = %v", err)
	}
	if durable.State != taskapi.StateCompleted || durable.Running || !durable.SupportsInput {
		t.Fatalf("durable Cancel race = %#v, want continuable completed terminal", durable)
	}
	runner.mu.Lock()
	cancelCalls := runner.cancelCalls
	runner.mu.Unlock()
	if cancelCalls != 1 {
		t.Fatalf("Runner.Cancel calls = %d, want one", cancelCalls)
	}

	// A later Cancel observes the already-proven terminal and cannot overwrite
	// it or issue another remote effect.
	again, err := runtime.tasks.Cancel(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindController,
	})
	if err != nil || again.State != taskapi.StateCompleted {
		t.Fatalf("Cancel(terminal) = %#v, %v; want unchanged completed", again, err)
	}
	runner.mu.Lock()
	cancelCalls = runner.cancelCalls
	runner.mu.Unlock()
	if cancelCalls != 1 {
		t.Fatalf("Runner.Cancel calls after terminal = %d, want one", cancelCalls)
	}
}

func TestSubagentCompletionBeforeCancelRemainsFirstTerminal(t *testing.T) {
	t.Parallel()

	runner := &cancelCompletionRaceRunner{
		result: delegation.Result{State: delegation.StateCancelled},
	}
	runtime, active := newSubagentTaskTestRuntime(t, runner)
	started, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		Agent: "reviewer", Prompt: "review",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	runner.mu.Lock()
	sink := runner.spawn.Completion
	runner.mu.Unlock()
	sink.PublishSubagentCompletion(delegation.Result{
		TaskID: started.Ref.TaskID, State: delegation.StateCompleted, Result: "completion first",
	})
	snapshot, err := runtime.tasks.Cancel(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindController,
	})
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if snapshot.State != taskapi.StateCompleted || snapshot.Running ||
		taskStringValue(snapshot.Result["final_message"]) != "completion first" {
		t.Fatalf("Cancel(after completion) = %#v, want first completed terminal", snapshot)
	}
	runner.mu.Lock()
	cancelCalls := runner.cancelCalls
	runner.mu.Unlock()
	if cancelCalls != 0 {
		t.Fatalf("Runner.Cancel calls = %d, want zero after terminal", cancelCalls)
	}
}

func TestSubagentCancelRestartClosesUnknownWithoutRemoteOrTaskObservation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "cancel-restart", PreferredSessionID: "cancel-restart",
	})
	if err != nil {
		t.Fatal(err)
	}
	tasks := sessionfile.NewTaskStore(sessions)
	const taskID = "cancel-restart-task"
	if _, err := tasks.Put(ctx, taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: taskID, Handle: "reviewer", Kind: taskapi.KindSubagent, Session: active.SessionRef,
		State: taskapi.StateUnknownOutcome, Running: true, SupportsCancel: true,
		Spec: map[string]any{
			"agent": "reviewer", "handle": "reviewer", "session_id": "lost-child",
			"agent_id": "lost-agent", "cancel_phase": string(subagentCancelPhaseApplied),
		},
		Result: map[string]any{"state": string(taskapi.StateUnknownOutcome)},
		Metadata: map[string]any{
			"state": string(taskapi.StateUnknownOutcome), "running": true,
			"cancel_phase": string(subagentCancelPhaseApplied),
		},
	}}); err != nil {
		t.Fatal(err)
	}

	reopenedSessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	runner := &cancelEffectProbeRunner{}
	restarted, err := New(testConfigWithACPForwarder(Config{
		Sessions: reopenedSessions, AgentFactory: chat.Factory{}, Subagents: runner,
		TaskStore: sessionfile.NewTaskStore(reopenedSessions),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.recoverRuntimeState(ctx, active.SessionRef); err != nil {
		t.Fatalf("recoverRuntimeState() error = %v", err)
	}
	recovered, err := sessionfile.NewTaskStore(
		sessionfile.NewStore(sessionfile.Config{RootDir: root}),
	).Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != taskapi.StateUnknownOutcome || recovered.Running || recovered.SupportsInput ||
		taskStringValue(recovered.Result["error"]) != subagentCancelRestartDiagnostic ||
		taskStringValue(recovered.Metadata["cancel_phase"]) != string(subagentCancelPhaseUnknown) {
		t.Fatalf("recovered cancel = %#v, want terminal bounded unknown outcome", recovered)
	}
	if runner.calls() != 0 || runner.waits() != 0 {
		t.Fatalf("restart recovery called Cancel=%d Wait=%d, want no remote observation/effect", runner.calls(), runner.waits())
	}
}

func TestSubagentCancelRestartClosesEveryEffectPhaseAsUnknown(t *testing.T) {
	t.Parallel()

	for _, phase := range []subagentCancelPhase{subagentCancelPhaseClaimed, subagentCancelPhaseUnknown} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			sessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := sessions.StartSession(ctx, session.StartSessionRequest{
				AppName: "caelis", UserID: "cancel-restart-" + string(phase),
			})
			if err != nil {
				t.Fatal(err)
			}
			tasks := sessionfile.NewTaskStore(sessions)
			taskID := "cancel-restart-" + string(phase)
			if _, err := tasks.Put(ctx, taskapi.PutRequest{Entry: &taskapi.Entry{
				TaskID: taskID, Handle: "reviewer", Kind: taskapi.KindSubagent, Session: active.SessionRef,
				State: taskapi.StateUnknownOutcome, Running: true, SupportsCancel: true,
				Spec: map[string]any{
					"agent": "reviewer", "handle": "reviewer", "session_id": "lost-child",
					"agent_id": "lost-agent", "cancel_phase": string(phase),
				},
				Result: map[string]any{"state": string(taskapi.StateUnknownOutcome)},
				Metadata: map[string]any{
					"state": string(taskapi.StateUnknownOutcome), "running": true,
					"cancel_phase": string(phase),
				},
			}}); err != nil {
				t.Fatal(err)
			}

			reopenedSessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			runner := &cancelEffectProbeRunner{}
			restarted, err := New(testConfigWithACPForwarder(Config{
				Sessions: reopenedSessions, AgentFactory: chat.Factory{}, Subagents: runner,
				TaskStore: sessionfile.NewTaskStore(reopenedSessions),
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.recoverRuntimeState(ctx, active.SessionRef); err != nil {
				t.Fatalf("recoverRuntimeState() error = %v", err)
			}
			recovered, err := sessionfile.NewTaskStore(
				sessionfile.NewStore(sessionfile.Config{RootDir: root}),
			).Get(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.State != taskapi.StateUnknownOutcome || recovered.Running || recovered.SupportsInput ||
				taskStringValue(recovered.Result["error"]) != subagentCancelRestartDiagnostic ||
				taskStringValue(recovered.Metadata["cancel_phase"]) != string(subagentCancelPhaseUnknown) {
				t.Fatalf("recovered cancel = %#v, want terminal bounded unknown outcome", recovered)
			}
			if runner.calls() != 0 || runner.waits() != 0 {
				t.Fatalf("restart recovery called Cancel=%d Wait=%d, want no remote observation/effect", runner.calls(), runner.waits())
			}
		})
	}
}
