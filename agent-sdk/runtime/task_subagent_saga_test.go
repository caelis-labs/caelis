package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	memory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

type sagaTaskStore struct {
	mu        sync.Mutex
	entries   map[string]*taskapi.Entry
	puts      int
	failOnPut int
}

func newSagaTaskStore() *sagaTaskStore { return &sagaTaskStore{entries: map[string]*taskapi.Entry{}} }

func (s *sagaTaskStore) Upsert(ctx context.Context, entry *taskapi.Entry) error {
	current, _ := s.Get(ctx, entry.TaskID)
	expected := uint64(0)
	if current != nil {
		expected = current.Revision
	}
	_, err := s.Put(ctx, taskapi.PutRequest{Entry: entry, ExpectedRevision: expected})
	return err
}

func (s *sagaTaskStore) Put(_ context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	if s.failOnPut > 0 && s.puts == s.failOnPut {
		return nil, fmt.Errorf("forced task persistence failure at put %d", s.puts)
	}
	current := s.entries[req.Entry.TaskID]
	actual := uint64(0)
	if current != nil {
		actual = current.Revision
	}
	if actual != req.ExpectedRevision {
		return nil, &taskapi.RevisionConflictError{TaskID: req.Entry.TaskID, Expected: req.ExpectedRevision, Actual: actual}
	}
	next := taskapi.CloneEntry(req.Entry)
	next.Revision = actual + 1
	s.entries[next.TaskID] = next
	return taskapi.CloneEntry(next), nil
}

func (s *sagaTaskStore) Get(_ context.Context, taskID string) (*taskapi.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[strings.TrimSpace(taskID)]
	if entry == nil {
		return nil, errors.New("not found")
	}
	return taskapi.CloneEntry(entry), nil
}

func (s *sagaTaskStore) ListSession(_ context.Context, ref session.SessionRef) ([]*taskapi.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*taskapi.Entry
	for _, entry := range s.entries {
		if entry.Session.SessionID == ref.SessionID {
			out = append(out, taskapi.CloneEntry(entry))
		}
	}
	return out, nil
}

func (s *sagaTaskStore) GetSessionTaskByHandle(ctx context.Context, ref session.SessionRef, kind taskapi.Kind, handle string) (*taskapi.Entry, error) {
	entries, _ := s.ListSession(ctx, ref)
	for _, entry := range entries {
		if entry.Kind == kind && taskSpecString(entry.Spec, "handle") == handle {
			return entry, nil
		}
	}
	return nil, errors.New("not found")
}

type sagaRunner struct {
	spawnCalls  int
	cancelCalls int
	cancelErr   error
}

func (r *sagaRunner) Spawn(_ context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	r.spawnCalls++
	return delegation.Anchor{TaskID: spawn.TaskID, SessionID: "child-saga", Agent: req.Agent, AgentID: "child-agent-saga"}, delegation.Result{TaskID: spawn.TaskID, State: delegation.StateCompleted, Result: "saga result"}, nil
}
func (*sagaRunner) Continue(context.Context, delegation.Anchor, delegation.ContinueRequest) (delegation.Result, error) {
	return delegation.Result{}, nil
}
func (*sagaRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	return delegation.Result{}, nil
}
func (r *sagaRunner) Cancel(context.Context, delegation.Anchor) error {
	r.cancelCalls++
	return r.cancelErr
}

type sagaSessionService struct {
	session.Service
	failParticipant bool
	failCanonical   bool
}

func (s *sagaSessionService) PutParticipantWithEvent(ctx context.Context, req session.PutParticipantWithEventRequest) (session.Session, *session.Event, error) {
	if s.failParticipant {
		return session.Session{}, nil, errors.New("forced participant lifecycle failure")
	}
	return s.Service.(session.ParticipantLifecycleService).PutParticipantWithEvent(ctx, req)
}
func (s *sagaSessionService) RemoveParticipantWithEvent(ctx context.Context, req session.RemoveParticipantWithEventRequest) (session.Session, *session.Event, error) {
	return s.Service.(session.ParticipantLifecycleService).RemoveParticipantWithEvent(ctx, req)
}
func (s *sagaSessionService) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if s.failCanonical && req.Event != nil && session.IsCanonicalHistoryEvent(req.Event) {
		return nil, errors.New("forced canonical dialogue failure")
	}
	return s.Service.AppendEvent(ctx, req)
}

func TestSubagentSpawnSagaCompensatesEveryPostSpawnBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		failPut         int
		failParticipant bool
		failCanonical   bool
		cancelErr       bool
		wantSpawn       int
		wantCancel      int
		wantStatus      string
	}{
		{name: "before spawn intent", failPut: 1, wantSpawn: 0, wantCancel: 0},
		{name: "after spawn before task commit", failPut: 3, wantSpawn: 1, wantCancel: 1, wantStatus: spawnStatusCompensated},
		{name: "after task before participant", failParticipant: true, wantSpawn: 1, wantCancel: 1, wantStatus: spawnStatusCompensated},
		{name: "after participant before dialogue", failCanonical: true, wantSpawn: 1, wantCancel: 1, wantStatus: spawnStatusCompensated},
		{name: "after dialogue before saga commit", failPut: 5, wantSpawn: 1, wantCancel: 1, wantStatus: spawnStatusCompensated},
		{name: "cancellation cannot prove termination", failParticipant: true, cancelErr: true, wantSpawn: 1, wantCancel: 1, wantStatus: spawnStatusUnknownOutcome},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := memory.NewService(memory.NewStore(memory.Config{}))
			active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "saga"})
			if err != nil {
				t.Fatal(err)
			}
			sessions := &sagaSessionService{Service: base, failParticipant: test.failParticipant, failCanonical: test.failCanonical}
			store := newSagaTaskStore()
			store.failOnPut = test.failPut
			runner := &sagaRunner{}
			if test.cancelErr {
				runner.cancelErr = errors.New("forced cancellation failure")
			}
			runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
				SpawnID: "spawn-fault", Agent: "helper", Prompt: "review", Source: "slash_agent", ParentTool: "slash",
			})
			if err == nil {
				t.Fatal("StartSubagent() error = nil, want injected saga failure")
			}
			if runner.spawnCalls != test.wantSpawn || runner.cancelCalls != test.wantCancel {
				t.Fatalf("spawn/cancel calls = %d/%d, want %d/%d", runner.spawnCalls, runner.cancelCalls, test.wantSpawn, test.wantCancel)
			}
			loaded, loadErr := sessions.Session(context.Background(), active.SessionRef)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if test.wantCancel > 0 && len(loaded.Participants) != 0 {
				t.Fatalf("participants after compensation = %#v, want none", loaded.Participants)
			}
			if test.wantStatus != "" {
				taskID, _ := subagentSpawnTaskID(active.SessionRef, "spawn-fault")
				entry, getErr := store.Get(context.Background(), taskID)
				if getErr != nil || taskStringValue(entry.Metadata["spawn_status"]) != test.wantStatus {
					t.Fatalf("durable spawn status = entry %#v error %v, want %q", entry, getErr, test.wantStatus)
				}
			}
		})
	}
}

func TestSubagentSpawnSagaRetryAndRestartNeverBlindlyRespawn(t *testing.T) {
	t.Parallel()

	base := memory.NewService(memory.NewStore(memory.Config{}))
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "saga"})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &sagaRunner{}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	req := taskapi.SubagentStartRequest{SpawnID: "stable-spawn", Agent: "helper", Prompt: "review", Source: "slash_agent", ParentTool: "slash"}
	first, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, req)
	if err != nil {
		t.Fatalf("first StartSubagent() error = %v", err)
	}
	second, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, req)
	if err != nil {
		t.Fatalf("idempotent StartSubagent() error = %v", err)
	}
	if runner.spawnCalls != 1 || first.Ref.TaskID != second.Ref.TaskID {
		t.Fatalf("idempotent spawn = calls %d task IDs %q/%q", runner.spawnCalls, first.Ref.TaskID, second.Ref.TaskID)
	}

	unknownStore := newSagaTaskStore()
	taskID, _ := subagentSpawnTaskID(active.SessionRef, "restart-spawn")
	_, err = unknownStore.Put(context.Background(), taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: taskID, Kind: taskapi.KindSubagent, Session: active.SessionRef, State: taskapi.StatePrepared,
		Spec:     map[string]any{"spawn_identity": "restart-spawn", "agent": "helper", "prompt": "review"},
		Metadata: map[string]any{"spawn_status": spawnStatusSpawning},
	}, ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	restartedRunner := &sagaRunner{}
	restarted, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: restartedRunner, TaskStore: unknownStore}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.tasks.StartSubagent(context.Background(), active, active.SessionRef, restartedRunner, taskapi.SubagentStartRequest{
		SpawnID: "restart-spawn", Agent: "helper", Prompt: "review",
	})
	if err == nil || restartedRunner.spawnCalls != 0 {
		t.Fatalf("restart StartSubagent() = error %v spawn calls %d, want unknown outcome without respawn", err, restartedRunner.spawnCalls)
	}

	spawnedStore := newSagaTaskStore()
	spawnedTaskID, _ := subagentSpawnTaskID(active.SessionRef, "spawned-restart")
	_, err = spawnedStore.Put(context.Background(), taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: spawnedTaskID, Kind: taskapi.KindSubagent, Session: active.SessionRef, State: taskapi.StateRunning, Running: true,
		Spec: map[string]any{
			"spawn_identity": "spawned-restart", "agent": "helper", "prompt": "review", "session_id": "child-restart",
			"agent_id": "child-agent-restart", "handle": "helper", "turn_seq": int64(1),
		},
		Metadata: map[string]any{"spawn_status": spawnStatusSpawned, "spawn_identity": "spawned-restart"},
	}, ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	spawnedRunner := &sagaRunner{}
	spawnedRuntime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: spawnedRunner, TaskStore: spawnedStore}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = spawnedRuntime.tasks.StartSubagent(context.Background(), active, active.SessionRef, spawnedRunner, taskapi.SubagentStartRequest{
		SpawnID: "spawned-restart", Agent: "helper", Prompt: "review",
	})
	if err == nil || spawnedRunner.spawnCalls != 0 || spawnedRunner.cancelCalls != 1 {
		t.Fatalf("spawned restart = error %v spawn/cancel %d/%d, want compensation without respawn", err, spawnedRunner.spawnCalls, spawnedRunner.cancelCalls)
	}
	spawnedEntry, err := spawnedStore.Get(context.Background(), spawnedTaskID)
	if err != nil || taskStringValue(spawnedEntry.Metadata["spawn_status"]) != spawnStatusCompensated {
		t.Fatalf("spawned restart durable entry = %#v, %v; want compensated", spawnedEntry, err)
	}
}
