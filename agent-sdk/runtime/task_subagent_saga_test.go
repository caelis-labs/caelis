package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	memory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

type sagaTaskStore struct {
	mu          sync.Mutex
	entries     map[string]*taskapi.Entry
	puts        int
	failOnPut   int
	commitOnPut int
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
	if s.commitOnPut > 0 && s.puts == s.commitOnPut {
		return taskapi.CloneEntry(next), &session.CommittedError{Err: fmt.Errorf("forced committed task persistence error at put %d", s.puts)}
	}
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
	failParticipant   bool
	commitParticipant bool
	failCanonical     bool
	failCanonicalAt   int
	canonicalCalls    int
	commitCanonical   bool
}

func (s *sagaSessionService) PutParticipantWithEvent(ctx context.Context, req session.PutParticipantWithEventRequest) (session.Session, *session.Event, error) {
	if s.failParticipant {
		return session.Session{}, nil, errors.New("forced participant lifecycle failure")
	}
	updated, event, err := s.Service.(session.ParticipantLifecycleService).PutParticipantWithEvent(ctx, req)
	if err == nil && s.commitParticipant {
		s.commitParticipant = false
		return updated, event, &session.CommittedError{Err: errors.New("forced committed participant error")}
	}
	return updated, event, err
}
func (s *sagaSessionService) RemoveParticipantWithEvent(ctx context.Context, req session.RemoveParticipantWithEventRequest) (session.Session, *session.Event, error) {
	return s.Service.(session.ParticipantLifecycleService).RemoveParticipantWithEvent(ctx, req)
}
func (s *sagaSessionService) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	canonical := req.Event != nil && session.IsCanonicalHistoryEvent(req.Event)
	if canonical {
		s.canonicalCalls++
		if s.failCanonical || (s.failCanonicalAt > 0 && s.canonicalCalls == s.failCanonicalAt) {
			return nil, errors.New("forced canonical dialogue failure")
		}
	}
	persisted, err := s.Service.AppendEvent(ctx, req)
	if err == nil && canonical && s.commitCanonical {
		s.commitCanonical = false
		return persisted, &session.CommittedError{Err: errors.New("forced committed canonical error")}
	}
	return persisted, err
}

func TestSubagentSpawnSagaCompensatesEveryPostSpawnBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		failPut         int
		failParticipant bool
		failCanonical   bool
		failCanonicalAt int
		cancelErr       bool
		wantSpawn       int
		wantCancel      int
		wantStatus      string
		wantParticipant bool
		rollForward     bool
	}{
		{name: "before spawn intent", failPut: 1, wantSpawn: 0, wantCancel: 0},
		{name: "after spawn before task commit", failPut: 3, wantSpawn: 1, wantCancel: 1, wantStatus: spawnStatusCompensated},
		{name: "after task before participant", failParticipant: true, wantSpawn: 1, wantCancel: 1, wantStatus: spawnStatusCompensated},
		{name: "after participant before canonical phase", failPut: 5, wantSpawn: 1, wantCancel: 1, wantStatus: spawnStatusCompensated},
		{name: "canonical user append failure", failCanonical: true, wantSpawn: 1, wantCancel: 0, wantStatus: spawnStatusCanonicalCommitting, wantParticipant: true, rollForward: true},
		{name: "after canonical user before final", failCanonicalAt: 2, wantSpawn: 1, wantCancel: 0, wantStatus: spawnStatusCanonicalCommitting, wantParticipant: true, rollForward: true},
		{name: "after canonical dialogue before phase commit", failPut: 7, wantSpawn: 1, wantCancel: 0, wantStatus: spawnStatusCanonicalCommitting, wantParticipant: true, rollForward: true},
		{name: "after canonical phase before committed", failPut: 8, wantSpawn: 1, wantCancel: 0, wantStatus: spawnStatusCanonicalCommitted, wantParticipant: true, rollForward: true},
		{name: "cancellation cannot prove termination", failParticipant: true, cancelErr: true, wantSpawn: 1, wantCancel: 1, wantStatus: spawnStatusUnknownOutcome},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := memory.NewService(memory.NewStore(memory.Config{}))
			active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "saga"})
			if err != nil {
				t.Fatal(err)
			}
			sessions := &sagaSessionService{Service: base, failParticipant: test.failParticipant, failCanonical: test.failCanonical, failCanonicalAt: test.failCanonicalAt}
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
				SpawnID: "spawn-fault", Agent: "helper", Prompt: "review", Source: "slash_agent", Role: session.ParticipantRoleSidecar,
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
			if test.wantParticipant && len(loaded.Participants) != 1 {
				t.Fatalf("participants = %#v, want durable attachment for roll-forward", loaded.Participants)
			}
			if test.wantStatus != "" {
				taskID, _ := subagentSpawnTaskID(active.SessionRef, "spawn-fault")
				entry, getErr := store.Get(context.Background(), taskID)
				if getErr != nil || taskStringValue(entry.Metadata["spawn_status"]) != test.wantStatus {
					t.Fatalf("durable spawn status = entry %#v error %v, want %q", entry, getErr, test.wantStatus)
				}
			}
			if test.rollForward {
				sessions.failCanonical = false
				sessions.failCanonicalAt = 0
				restarted, restartErr := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
				if restartErr != nil {
					t.Fatal(restartErr)
				}
				_, retryErr := restarted.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
					SpawnID: "spawn-fault", Agent: "helper", Prompt: "review", Source: "slash_agent", Role: session.ParticipantRoleSidecar,
				})
				if retryErr != nil {
					t.Fatalf("roll-forward retry error = %v", retryErr)
				}
				if runner.spawnCalls != 1 || runner.cancelCalls != 0 {
					t.Fatalf("roll-forward spawn/cancel calls = %d/%d, want 1/0", runner.spawnCalls, runner.cancelCalls)
				}
				taskID, _ := subagentSpawnTaskID(active.SessionRef, "spawn-fault")
				entry, getErr := store.Get(context.Background(), taskID)
				if getErr != nil || taskStringValue(entry.Metadata["spawn_status"]) != spawnStatusCommitted {
					t.Fatalf("rolled-forward entry = %#v, %v", entry, getErr)
				}
			}
			assertSubagentSagaModelRoundTrip(t, sessions, active.SessionRef)
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
	req := taskapi.SubagentStartRequest{SpawnID: "stable-spawn", Agent: "helper", Prompt: "review", Source: "slash_agent", Role: session.ParticipantRoleSidecar}
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
		SpawnID: "spawned-restart", Agent: "helper", Prompt: "review", Role: session.ParticipantRoleSidecar,
	})
	if err != nil || spawnedRunner.spawnCalls != 0 || spawnedRunner.cancelCalls != 0 {
		t.Fatalf("spawned restart = error %v spawn/cancel %d/%d, want roll-forward without respawn", err, spawnedRunner.spawnCalls, spawnedRunner.cancelCalls)
	}
	spawnedEntry, err := spawnedStore.Get(context.Background(), spawnedTaskID)
	if err != nil || taskStringValue(spawnedEntry.Metadata["spawn_status"]) != spawnStatusCommitted {
		t.Fatalf("spawned restart durable entry = %#v, %v; want committed", spawnedEntry, err)
	}
}

func TestSubagentSpawnRequiresCASBeforeExternalEffect(t *testing.T) {
	t.Parallel()

	base := memory.NewService(memory.NewStore(memory.Config{}))
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "upsert-only"})
	if err != nil {
		t.Fatal(err)
	}
	runner := &sagaRunner{}
	store := &upsertOnlySagaStore{base: newSagaTaskStore()}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{SpawnID: "upsert-only", Agent: "helper", Prompt: "review"})
	if err == nil || !strings.Contains(err.Error(), "CASStore") || runner.spawnCalls != 0 {
		t.Fatalf("StartSubagent() = %v, spawn calls %d; want fail closed before spawn", err, runner.spawnCalls)
	}
}

func TestConcurrentSubagentSpawnCASCallsExternalSpawnOnce(t *testing.T) {
	t.Parallel()

	base := memory.NewService(memory.NewStore(memory.Config{}))
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "concurrent-spawn"})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &countingSagaRunner{}
	newRuntime := func() *Runtime {
		runtime, runtimeErr := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
		if runtimeErr != nil {
			t.Fatal(runtimeErr)
		}
		return runtime
	}
	runtimes := []*Runtime{newRuntime(), newRuntime()}
	start := make(chan struct{})
	errs := make(chan error, len(runtimes))
	var wg sync.WaitGroup
	for _, runtime := range runtimes {
		wg.Add(1)
		go func(runtime *Runtime) {
			defer wg.Done()
			<-start
			_, startErr := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
				SpawnID: "shared-spawn", Agent: "helper", Prompt: "review", Role: session.ParticipantRoleSidecar,
			})
			errs <- startErr
		}(runtime)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		var conflict *taskapi.RevisionConflictError
		if err != nil && !errors.As(err, &conflict) && !strings.Contains(err.Error(), "claimed concurrently") && !strings.Contains(err.Error(), "refusing blind respawn") {
			t.Fatalf("concurrent StartSubagent() error = %v", err)
		}
	}
	if calls := runner.spawnCalls.Load(); calls != 1 {
		t.Fatalf("external Spawn() calls = %d, want 1", calls)
	}
}

func TestSubagentSpawnCommittedErrorsReloadAndRollForward(t *testing.T) {
	t.Parallel()

	base := memory.NewService(memory.NewStore(memory.Config{}))
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "committed-errors"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := &sagaSessionService{Service: base, commitParticipant: true, commitCanonical: true}
	store := newSagaTaskStore()
	store.commitOnPut = 4
	runner := &sagaRunner{}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "committed-errors", Agent: "helper", Prompt: "review", Role: session.ParticipantRoleSidecar,
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	entry, err := store.Get(context.Background(), snapshot.Ref.TaskID)
	if err != nil || taskStringValue(entry.Metadata["spawn_status"]) != spawnStatusCommitted {
		t.Fatalf("entry = %#v, %v, want committed", entry, err)
	}
	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Session.Participants) != 1 || runner.spawnCalls != 1 || runner.cancelCalls != 0 {
		t.Fatalf("participants=%#v spawn/cancel=%d/%d", loaded.Session.Participants, runner.spawnCalls, runner.cancelCalls)
	}
	assertSubagentSagaModelRoundTrip(t, sessions, active.SessionRef)
}

func TestSubagentSpawnSagaFileRoundTripWholeObjects(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	sessions := sessionfile.NewService(store)
	tasks := sessionfile.NewTaskStore(store)
	active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "file-roundtrip", PreferredSessionID: "subagent-saga-roundtrip",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &sagaRunner{}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: tasks}))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "file-roundtrip", Agent: "helper", Prompt: "review", Source: "test", Role: session.ParticipantRoleSidecar,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantTask, err := tasks.Get(context.Background(), snapshot.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	wantSession, err := sessions.Session(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	wantEvents, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}

	reopenedStore := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	reopenedSessions := sessionfile.NewService(reopenedStore)
	reopenedTasks := sessionfile.NewTaskStore(reopenedStore)
	gotTask, err := reopenedTasks.Get(context.Background(), snapshot.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	gotSession, err := reopenedSessions.Session(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	gotEvents, err := reopenedSessions.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotTask, wantTask) {
		t.Fatalf("reopened task = %#v, want %#v", gotTask, wantTask)
	}
	if !reflect.DeepEqual(gotSession, wantSession) {
		t.Fatalf("reopened session binding = %#v, want %#v", gotSession, wantSession)
	}
	if !reflect.DeepEqual(gotEvents, wantEvents) {
		t.Fatalf("reopened canonical dialogue = %#v, want %#v", gotEvents, wantEvents)
	}
	assertSubagentSagaModelRoundTrip(t, reopenedSessions, active.SessionRef)
}

type upsertOnlySagaStore struct{ base *sagaTaskStore }

func (s *upsertOnlySagaStore) Upsert(ctx context.Context, entry *taskapi.Entry) error {
	return s.base.Upsert(ctx, entry)
}
func (s *upsertOnlySagaStore) Get(ctx context.Context, taskID string) (*taskapi.Entry, error) {
	return s.base.Get(ctx, taskID)
}
func (s *upsertOnlySagaStore) ListSession(ctx context.Context, ref session.SessionRef) ([]*taskapi.Entry, error) {
	return s.base.ListSession(ctx, ref)
}
func (s *upsertOnlySagaStore) GetSessionTaskByHandle(ctx context.Context, ref session.SessionRef, kind taskapi.Kind, handle string) (*taskapi.Entry, error) {
	return s.base.GetSessionTaskByHandle(ctx, ref, kind, handle)
}

type countingSagaRunner struct{ spawnCalls atomic.Int32 }

func (r *countingSagaRunner) Spawn(_ context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	r.spawnCalls.Add(1)
	time.Sleep(10 * time.Millisecond)
	return delegation.Anchor{TaskID: spawn.TaskID, SessionID: "child-concurrent", Agent: req.Agent, AgentID: "child-agent-concurrent"}, delegation.Result{TaskID: spawn.TaskID, State: delegation.StateCompleted, Result: "done"}, nil
}
func (*countingSagaRunner) Continue(context.Context, delegation.Anchor, delegation.ContinueRequest) (delegation.Result, error) {
	return delegation.Result{}, nil
}
func (*countingSagaRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	return delegation.Result{}, nil
}
func (*countingSagaRunner) Cancel(context.Context, delegation.Anchor) error { return nil }

func assertSubagentSagaModelRoundTrip(t *testing.T, sessions session.Service, ref session.SessionRef) {
	t.Helper()
	firstProbe := &recoveryCaptureModel{}
	first, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.Run(context.Background(), agent.RunRequest{SessionRef: ref, Input: "round-trip probe one", AgentSpec: agent.AgentSpec{Name: "chat", Model: firstProbe}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainRunnerEvents(t, run.Handle); err != nil {
		t.Fatal(err)
	}
	secondProbe := &recoveryCaptureModel{}
	second, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	run, err = second.Run(context.Background(), agent.RunRequest{SessionRef: ref, Input: "round-trip probe two", AgentSpec: agent.AgentSpec{Name: "chat", Model: secondProbe}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainRunnerEvents(t, run.Handle); err != nil {
		t.Fatal(err)
	}
	want := append(model.CloneMessages(firstProbe.messages), model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, "round-trip probe two"))
	if !reflect.DeepEqual(secondProbe.messages, want) {
		t.Fatalf("rebuilt model context = %#v, want live-produced %#v", secondProbe.messages, want)
	}
}
