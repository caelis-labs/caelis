package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

type cancelPhaseFailTaskStore struct {
	*sagaTaskStore
	mu        sync.Mutex
	failPhase subagentCancelPhase
	failed    bool
	err       error
}

func (s *cancelPhaseFailTaskStore) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	s.mu.Lock()
	phase := subagentCancelPhase(taskStringValue(req.Entry.Metadata["cancel_phase"]))
	if !s.failed && phase == s.failPhase {
		s.failed = true
		err := s.err
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	return s.sagaTaskStore.Put(ctx, req)
}

type cancelEffectProbeRunner struct {
	mu          sync.Mutex
	cancelCalls int
	waitResult  delegation.Result
}

type naturalTerminalCancelRunner struct {
	mu          sync.Mutex
	terminal    delegation.Result
	waitCalls   int
	cancelCalls int
}

type cancelStateAuditTaskStore struct {
	*sagaTaskStore
	mu     sync.Mutex
	writes []*taskapi.Entry
}

type completionHandoffCancelRunner struct {
	terminal delegation.Result
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	mu       sync.Mutex
	calls    int
}

func (*cancelEffectProbeRunner) Spawn(context.Context, subagent.SpawnContext, delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return delegation.Anchor{}, delegation.Result{}, errors.New("unexpected spawn")
}

func (r *cancelEffectProbeRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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

func (*naturalTerminalCancelRunner) Spawn(context.Context, subagent.SpawnContext, delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return delegation.Anchor{}, delegation.Result{}, errors.New("unexpected spawn")
}

func (r *naturalTerminalCancelRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waitCalls++
	if r.waitCalls == 1 {
		return delegation.Result{State: delegation.StateRunning, Running: true}, nil
	}
	return delegation.CloneResult(r.terminal), nil
}

func (r *naturalTerminalCancelRunner) Cancel(context.Context, delegation.Anchor) error {
	r.mu.Lock()
	r.cancelCalls++
	r.mu.Unlock()
	return nil
}

func (r *naturalTerminalCancelRunner) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelCalls
}

func (s *cancelStateAuditTaskStore) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	persisted, err := s.sagaTaskStore.Put(ctx, req)
	if err == nil && persisted != nil {
		s.mu.Lock()
		s.writes = append(s.writes, taskapi.CloneEntry(persisted))
		s.mu.Unlock()
	}
	return persisted, err
}

func (s *cancelStateAuditTaskStore) states() []taskapi.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make([]taskapi.State, 0, len(s.writes))
	for _, entry := range s.writes {
		states = append(states, entry.State)
	}
	return states
}

func (r *completionHandoffCancelRunner) Spawn(_ context.Context, spawn subagent.SpawnContext, _ delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return delegation.Anchor{
			TaskID: spawn.TaskID, SessionID: "child-handoff", AgentID: "agent-handoff",
		}, delegation.Result{
			TaskID: spawn.TaskID, State: delegation.StateRunning, Running: true,
		}, nil
}

func (r *completionHandoffCancelRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	return delegation.CloneResult(r.terminal), nil
}

func (r *completionHandoffCancelRunner) Cancel(context.Context, delegation.Anchor) error {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return nil
}

func (r *completionHandoffCancelRunner) cancelCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestSubagentCancelResultClassification(t *testing.T) {
	t.Parallel()

	pending := []delegation.Result{
		{State: delegation.StateRunning},
		{State: delegation.StateCompleted, Running: true},
		{State: delegation.StateWaitingApproval},
	}
	for _, result := range pending {
		if !subagentCancelResultPending(result) || subagentCancelResultTerminal(result) && !result.Running {
			t.Fatalf("result %#v was not classified as pending", result)
		}
	}
	for _, state := range []delegation.State{
		delegation.StateCompleted,
		delegation.StateFailed,
		delegation.StateCancelled,
		delegation.StateInterrupted,
		delegation.StateUnknownOutcome,
	} {
		result := delegation.Result{State: state}
		if subagentCancelResultPending(result) || !subagentCancelResultTerminal(result) {
			t.Fatalf("state %q was not classified as terminal", state)
		}
	}
	invalid := delegation.Result{State: delegation.State("invalid")}
	if subagentCancelResultPending(invalid) || subagentCancelResultTerminal(invalid) {
		t.Fatalf("invalid result %#v was classified as actionable", invalid)
	}
}

func TestSubagentCancelDefersNaturalTerminalUntilTargetGenerationObserved(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		terminal delegation.Result
	}{
		{
			name: "completed",
			terminal: delegation.Result{
				State: delegation.StateCompleted, Result: "natural final response",
			},
		},
		{
			name: "failed",
			terminal: delegation.Result{
				State: delegation.StateFailed, Error: "natural child failure",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runner := &naturalTerminalCancelRunner{terminal: test.terminal}
			store := &cancelStateAuditTaskStore{sagaTaskStore: newSagaTaskStore()}
			runtime, active := newSubagentCompletionSagaRuntime(t, store, nil, runner)
			entry := &taskapi.Entry{
				TaskID: "natural-terminal-" + test.name, Kind: taskapi.KindSubagent, Session: active.SessionRef,
				State: taskapi.StateCompleted, SupportsCancel: true,
				Spec: map[string]any{
					"agent": "reviewer", "session_id": "child-natural", "agent_id": "child-agent",
					"handle": "reviewer-1", "spawn_phase": spawnStatusCommitted, "turn_seq": int64(1),
					"spawn_result": map[string]any{"state": string(taskapi.StateCompleted), "final_message": "prior result"},
				},
				Result: map[string]any{"state": string(taskapi.StateCompleted), "final_message": "prior result", "handle": "reviewer-1"},
				Metadata: map[string]any{
					"state": string(taskapi.StateCompleted), "running": false, "handle": "reviewer-1",
					"spawn_status": spawnStatusCommitted, "turn_seq": int64(1),
				},
			}
			if _, err := store.Put(context.Background(), taskapi.PutRequest{Entry: entry}); err != nil {
				t.Fatal(err)
			}
			req := taskapi.ControlRequest{TaskID: entry.TaskID, Principal: session.ActorKindController}
			snapshot, err := runtime.tasks.Cancel(context.Background(), active.SessionRef, req)
			if err != nil {
				t.Fatal(err)
			}
			if !snapshot.Running || snapshot.State != taskapi.StateUnknownOutcome {
				t.Fatalf("Cancel() snapshot = %#v, want pending target generation", snapshot)
			}
			if got := taskRawStringValue(snapshot.Result["final_message"]); got != "" {
				t.Fatalf("Cancel() exposed future-generation final = %q", got)
			}
			if turnSeq, _ := taskInt64Value(snapshot.Metadata["turn_seq"]); turnSeq != 1 {
				t.Fatalf("Cancel() Task Turn = %d, want prior observed Turn 1", turnSeq)
			}
			if runner.calls() != 1 {
				t.Fatalf("Runner.Cancel calls = %d, want one endpoint request", runner.calls())
			}

			stored, err := store.Get(context.Background(), entry.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if !stored.Running || stored.State != taskapi.StateUnknownOutcome {
				t.Fatalf("durable cancellation = %#v, want pending target generation", stored)
			}
			if turnSeq := taskTurnSeqFromSpec(stored.Spec); turnSeq != 1 {
				t.Fatalf("durable Task Turn = %d, want prior observed Turn 1", turnSeq)
			}
			spawned, ok := stored.Spec["spawn_result"].(map[string]any)
			if !ok || taskStringValue(spawned["state"]) != string(taskapi.StateUnknownOutcome) ||
				taskRawStringValue(spawned["final_message"]) != "" {
				t.Fatalf("durable spawn_result = %#v, want no future-generation final", stored.Spec["spawn_result"])
			}
			if phase := taskStringValue(stored.Metadata[subagentCancelPhaseKey]); phase != string(subagentCancelPhaseApplied) {
				t.Fatalf("durable cancel phase = %q, want pending reconciliation", phase)
			}
			if cancelTurnSeq, ok := subagentCancelTurnSeq(stored.Metadata); !ok || cancelTurnSeq != 2 {
				t.Fatalf("durable cancel Turn = %d, %v; want 2", cancelTurnSeq, ok)
			}
			for _, state := range store.states() {
				if state == taskapi.StateCancelled {
					t.Fatalf("durable cancellation race transitioned through false cancelled state: %v", store.states())
				}
			}
		})
	}
}

func TestSubagentCancelCompletionHandoffNeverPersistsFalseCancellation(t *testing.T) {
	t.Parallel()

	terminal := delegation.Result{State: delegation.StateCompleted, Result: "natural completion during cancel"}
	runner := &completionHandoffCancelRunner{
		terminal: terminal, entered: make(chan struct{}), release: make(chan struct{}),
	}
	store := &cancelStateAuditTaskStore{sagaTaskStore: newSagaTaskStore()}
	runtime, active := newSubagentCompletionSagaRuntime(t, store, nil, runner)
	started, err := runtime.tasks.StartSubagent(
		context.Background(), active, active.SessionRef, runner,
		taskapi.SubagentStartRequest{Agent: "helper", Prompt: "inspect"},
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct {
		snapshot taskapi.Snapshot
		err      error
	}, 1)
	go func() {
		snapshot, cancelErr := runtime.tasks.Cancel(context.Background(), active.SessionRef, taskapi.ControlRequest{
			TaskID: started.Ref.TaskID, Principal: session.ActorKindTool, Source: "agent_tool",
		})
		cancelled <- struct {
			snapshot taskapi.Snapshot
			err      error
		}{snapshot: snapshot, err: cancelErr}
	}()
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		close(runner.release)
		t.Fatal("Runner.Cancel did not acquire the Task cancellation claim")
	}

	runtime.tasks.mu.RLock()
	current := runtime.tasks.subagents[started.Ref.TaskID]
	runtime.tasks.mu.RUnlock()
	current.mu.Lock()
	turnSeq := current.turnSeq
	current.mu.Unlock()
	observed := delegation.CloneResult(terminal)
	observed.TaskID = started.Ref.TaskID
	completionDone := newObservedSubagentCompletionSink(
		context.Background(), runtime.tasks, started.Ref.TaskID, turnSeq,
	).enqueue(observed)
	if completionDone == nil {
		close(runner.release)
		t.Fatal("observed terminal completion was not queued")
	}
	close(runner.release)

	var outcome struct {
		snapshot taskapi.Snapshot
		err      error
	}
	select {
	case outcome = <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("Task cancel did not finish after endpoint no-op")
	}
	if outcome.err != nil || outcome.snapshot.Running || outcome.snapshot.State != taskapi.StateCompleted ||
		taskRawStringValue(outcome.snapshot.Result["final_message"]) != terminal.Result {
		t.Fatalf("Cancel() = %#v, %v; want natural completion", outcome.snapshot, outcome.err)
	}
	select {
	case <-completionDone:
	case <-time.After(time.Second):
		t.Fatal("queued observed completion did not take the released Task claim")
	}
	if runner.cancelCalls() != 1 {
		t.Fatalf("Runner.Cancel calls = %d, want one endpoint no-op", runner.cancelCalls())
	}
	stored, err := store.Get(context.Background(), started.Ref.TaskID)
	if err != nil || stored.Running || stored.State != taskapi.StateCompleted {
		t.Fatalf("durable completion = %#v, %v; want natural result", stored, err)
	}
	spawned, ok := stored.Spec["spawn_result"].(map[string]any)
	if !ok || taskRawStringValue(spawned["final_message"]) != terminal.Result ||
		taskStringValue(spawned["state"]) != string(taskapi.StateCompleted) {
		t.Fatalf("durable spawn_result = %#v, want natural completion", stored.Spec["spawn_result"])
	}
	for _, state := range store.states() {
		if state == taskapi.StateCancelled {
			t.Fatalf("completion handoff persisted false cancelled state: %v", store.states())
		}
	}
}

func TestSubagentCancelTerminalPersistenceFailureDoesNotRepeatRemoteEffect(t *testing.T) {
	t.Parallel()

	runner := &cancelEffectProbeRunner{waitResult: delegation.Result{State: delegation.StateCancelled, Result: "cancelled"}}
	runtime, active := newSubagentTaskTestRuntime(t, runner)
	storeErr := errors.New("forced terminal cancel persistence failure")
	store := &cancelPhaseFailTaskStore{
		sagaTaskStore: newSagaTaskStore(), failPhase: subagentCancelPhaseCompleted, err: storeErr,
	}
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
	if !errors.Is(err, storeErr) || first.Ref.TaskID != entry.TaskID || runner.calls() != 1 {
		t.Fatalf("first Cancel() = %#v, %v calls=%d; want one remote effect and reachable task", first, err, runner.calls())
	}
	runtime.tasks.mu.RLock()
	stale := runtime.tasks.subagents[entry.TaskID]
	runtime.tasks.mu.RUnlock()
	if stale != nil {
		t.Fatal("ordinary non-committed persistence failure left a same-revision split cache installed")
	}
	second, err := runtime.tasks.Cancel(context.Background(), active.SessionRef, req)
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls() != 1 || second.State != taskapi.StateCancelled || second.Running {
		t.Fatalf("second Cancel() = %#v calls=%d; want durable reconciliation without re-Cancel", second, runner.calls())
	}
}
