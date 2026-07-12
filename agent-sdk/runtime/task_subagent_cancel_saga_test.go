package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"

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

func (*cancelEffectProbeRunner) Spawn(context.Context, subagent.SpawnContext, delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return delegation.Anchor{}, delegation.Result{}, errors.New("unexpected spawn")
}

func (*cancelEffectProbeRunner) Continue(context.Context, delegation.Anchor, delegation.ContinueRequest) (delegation.Result, error) {
	return delegation.Result{}, errors.New("unexpected continue")
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
