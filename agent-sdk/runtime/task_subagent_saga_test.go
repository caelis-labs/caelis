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
	failStatus  string
	failedState bool
}

type getFailingSagaTaskStore struct {
	*sagaTaskStore
	err error
}

func (s *getFailingSagaTaskStore) Get(context.Context, string) (*taskapi.Entry, error) {
	return nil, s.err
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
	if !s.failedState && s.failStatus != "" && taskStringValue(req.Entry.Metadata["spawn_status"]) == s.failStatus {
		s.failedState = true
		return nil, fmt.Errorf("forced task status persistence failure at %s", s.failStatus)
	}
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
	failDetach        bool
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
	if s.failDetach {
		return session.Session{}, nil, errors.New("forced participant detach failure")
	}
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

	// Durable put sequence for a successful sidecar spawn:
	// 1 intent, 2 external claim, 3 post_spawn, 4 final_event_persisted (completed), 5 committed.
	// Failures after remote spawn but before post_spawn commit compensate.
	// Canonical dialogue failures leave post_spawn and roll-forward without respawn.
	tests := []struct {
		name            string
		failPut         int
		failParticipant bool
		failCanonical   bool
		failCanonicalAt int
		failStatus      string
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
		{name: "canonical user append failure", failCanonical: true, wantSpawn: 1, wantCancel: 0, wantStatus: spawnStatusSpawned, wantParticipant: true, rollForward: true},
		{name: "after canonical user before final", failCanonicalAt: 2, wantSpawn: 1, wantCancel: 0, wantStatus: spawnStatusSpawned, wantParticipant: true, rollForward: true},
		{name: "after dialogue before committed mark", failStatus: spawnStatusCommitted, wantSpawn: 1, wantCancel: 0, wantStatus: spawnStatusSpawned, wantParticipant: true, rollForward: true},
		{name: "cancellation cannot prove termination", failParticipant: true, cancelErr: true, wantSpawn: 1, wantCancel: 1, wantStatus: spawnStatusUnknownOutcome},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := memory.NewStore(memory.Config{})
			active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "saga"})
			if err != nil {
				t.Fatal(err)
			}
			sessions := &sagaSessionService{Service: base, failParticipant: test.failParticipant, failCanonical: test.failCanonical, failCanonicalAt: test.failCanonicalAt}
			store := newSagaTaskStore()
			store.failOnPut = test.failPut
			store.failStatus = test.failStatus
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

func TestSubagentSpawnRejectsAgentIDCollisionWithoutReplacingOriginalParticipant(t *testing.T) {
	t.Parallel()
	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "participant-collision",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &sagaRunner{}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	first, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "collision-one", Agent: "helper", Prompt: "first", Role: session.ParticipantRoleSidecar,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "collision-two", Agent: "helper", Prompt: "second", Role: session.ParticipantRoleSidecar,
	})
	var conflict *session.ParticipantBindingConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("second spawn error = %v, want participant delegation conflict", err)
	}
	loaded, err := base.Session(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	binding, ok := participantBinding(loaded, "child-agent-saga")
	if !ok || binding.DelegationID != first.Ref.TaskID {
		t.Fatalf("participant after collision = %#v, want original delegation %q", binding, first.Ref.TaskID)
	}
	collidingTaskID, err := subagentSpawnTaskID(active.SessionRef, "collision-two")
	if err != nil {
		t.Fatal(err)
	}
	colliding, err := store.Get(context.Background(), collidingTaskID)
	if err != nil || taskStringValue(colliding.Metadata["spawn_status"]) != string(spawnPhaseCompensated) {
		t.Fatalf("colliding task = %#v, %v; want terminal compensated", colliding, err)
	}
	_, retryErr := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "collision-two", Agent: "helper", Prompt: "second", Role: session.ParticipantRoleSidecar,
	})
	if retryErr == nil || !strings.Contains(retryErr.Error(), "was compensated") {
		t.Fatalf("collision retry error = %v, want stable compensated terminal", retryErr)
	}
}

func TestSubagentSpawnSagaRetryAndRestartNeverBlindlyRespawn(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
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
	restartReq := taskapi.SubagentStartRequest{SpawnID: "restart-spawn", Agent: "helper", Prompt: "review"}
	restartDigest, err := subagentSpawnRequestDigest(restartReq, runtime.defaultPolicyMode, session.ParticipantRoleDelegated)
	if err != nil {
		t.Fatal(err)
	}
	_, err = unknownStore.Put(context.Background(), taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: taskID, Kind: taskapi.KindSubagent, Session: active.SessionRef, State: taskapi.StatePrepared,
		Spec: map[string]any{
			"spawn_identity": "restart-spawn", "spawn_request_digest": restartDigest,
			"agent": "helper", "prompt": "review",
		},
		Metadata: map[string]any{"spawn_status": spawnStatusSpawning, "spawn_request_digest": restartDigest},
	}, ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	restartedRunner := &sagaRunner{}
	restarted, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: restartedRunner, TaskStore: unknownStore}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.tasks.StartSubagent(context.Background(), active, active.SessionRef, restartedRunner, restartReq)
	if err == nil || restartedRunner.spawnCalls != 0 {
		t.Fatalf("restart StartSubagent() = error %v spawn calls %d, want unknown outcome without respawn", err, restartedRunner.spawnCalls)
	}
	unknownEntry, getErr := unknownStore.Get(context.Background(), taskID)
	if getErr != nil || taskStringValue(unknownEntry.Metadata["spawn_status"]) != spawnStatusUnknownOutcome || unknownEntry.State != taskapi.StateUnknownOutcome {
		t.Fatalf("spawning recovery entry = %#v, %v; want durable unknown outcome", unknownEntry, getErr)
	}

	spawnedStore := newSagaTaskStore()
	spawnedTaskID, _ := subagentSpawnTaskID(active.SessionRef, "spawned-restart")
	spawnedReq := taskapi.SubagentStartRequest{
		SpawnID: "spawned-restart", Agent: "helper", Prompt: "review", Role: session.ParticipantRoleSidecar,
	}
	spawnedDigest, err := subagentSpawnRequestDigest(spawnedReq, runtime.defaultPolicyMode, session.ParticipantRoleSidecar)
	if err != nil {
		t.Fatal(err)
	}
	_, err = spawnedStore.Put(context.Background(), taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: spawnedTaskID, Kind: taskapi.KindSubagent, Session: active.SessionRef, State: taskapi.StateRunning, Running: true,
		Spec: map[string]any{
			"spawn_identity": "spawned-restart", "spawn_request_digest": spawnedDigest,
			"agent": "helper", "prompt": "review", "session_id": "child-restart",
			"agent_id": "child-agent-restart", "handle": "helper", "turn_seq": int64(1),
		},
		Metadata: map[string]any{
			"spawn_status": spawnStatusSpawned, "spawn_identity": "spawned-restart", "spawn_request_digest": spawnedDigest,
		},
	}, ExpectedRevision: 0})
	if err != nil {
		t.Fatal(err)
	}
	spawnedRunner := &sagaRunner{}
	spawnedRuntime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: spawnedRunner, TaskStore: spawnedStore}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = spawnedRuntime.tasks.StartSubagent(context.Background(), active, active.SessionRef, spawnedRunner, spawnedReq)
	if err != nil || spawnedRunner.spawnCalls != 0 || spawnedRunner.cancelCalls != 0 {
		t.Fatalf("spawned restart = error %v spawn/cancel %d/%d, want roll-forward without respawn", err, spawnedRunner.spawnCalls, spawnedRunner.cancelCalls)
	}
	spawnedEntry, err := spawnedStore.Get(context.Background(), spawnedTaskID)
	if err != nil || taskStringValue(spawnedEntry.Metadata["spawn_status"]) != spawnStatusCommitted {
		t.Fatalf("spawned restart durable entry = %#v, %v; want committed", spawnedEntry, err)
	}
}

func TestSubagentSpawnCompensationResumesDetachBeforeTerminalState(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "detach-recovery"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := &sagaSessionService{Service: base, failDetach: true}
	store := newSagaTaskStore()
	runner := &sagaRunner{}
	req := taskapi.SubagentStartRequest{SpawnID: "detach-recovery", Agent: "helper", Prompt: "review", Role: session.ParticipantRoleSidecar}
	taskID, err := subagentSpawnTaskID(active.SessionRef, req.SpawnID)
	if err != nil {
		t.Fatal(err)
	}
	// Digest must match StartSubagent's mode defaulting (empty Mode uses runtime defaultPolicyMode).
	probe, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	mode := strings.TrimSpace(probe.defaultPolicyMode)
	digest, err := subagentSpawnRequestDigest(req, mode, session.ParticipantRoleSidecar)
	if err != nil {
		t.Fatal(err)
	}
	// Seed mid-compensation after a durable post-spawn attach, before detach.
	// Pure intermediate marker failures no longer exist; resume from compensating.
	lifecycle := sessions.Service.(session.ParticipantLifecycleService)
	if _, _, err := lifecycle.PutParticipantWithEvent(context.Background(), session.PutParticipantWithEventRequest{
		SessionRef: active.SessionRef,
		Binding: session.ParticipantBinding{
			ID: "child-agent-saga", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleSidecar,
			AgentName: "helper", Label: "@helper", SessionID: "child-saga", DelegationID: taskID, AttachedAt: time.Now(),
		},
		Event: &session.Event{
			Type: session.EventTypeParticipant, Visibility: session.VisibilityMirror, Time: time.Now(),
			Protocol: ptrEventProtocol(session.NewParticipantProtocol(session.ProtocolParticipant{Action: "attached"})),
			Scope:    &session.EventScope{Participant: session.ParticipantRef{ID: "child-agent-saga", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleSidecar, DelegationID: taskID}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := store.Put(context.Background(), taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: taskID, Kind: taskapi.KindSubagent, Session: active.SessionRef, Title: "SPAWN helper",
		State: taskapi.StateRunning, CreatedAt: now, UpdatedAt: now, SupportsCancel: true, Running: true,
		Spec: map[string]any{
			"spawn_identity": req.SpawnID, "spawn_request_digest": digest, "agent": "helper", "prompt": "review",
			"participant_role": string(session.ParticipantRoleSidecar), "handle": "helper",
			"session_id": "child-saga", "agent_id": "child-agent-saga", "terminal_id": subagentTerminalID(taskID),
			"spawn_phase": string(spawnPhaseCompensating),
		},
		Metadata: map[string]any{
			"spawn_status": string(spawnPhaseCompensating), "spawn_identity": req.SpawnID,
			"spawn_request_digest": digest, "spawn_reason": "forced compensation", "participant_role": string(session.ParticipantRoleSidecar),
		},
		Result: map[string]any{"state": string(taskapi.StateRunning), "error": "forced compensation"},
	}, ExpectedRevision: 0}); err != nil {
		t.Fatal(err)
	}

	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, req); err == nil {
		t.Fatal("StartSubagent() error = nil, want detach failure during compensation resume")
	}
	if runner.cancelCalls != 1 {
		t.Fatalf("cancel calls after first resume = %d, want 1", runner.cancelCalls)
	}
	loaded, err := sessions.Session(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Participants) != 1 {
		t.Fatalf("participants after failed detach = %#v, want recoverable attachment", loaded.Participants)
	}
	entry, err := store.Get(context.Background(), taskID)
	if err != nil || taskStringValue(entry.Metadata["spawn_status"]) != spawnStatusChildCancelled {
		t.Fatalf("after failed detach entry = %#v, %v, want child_cancelled", entry, err)
	}

	sessions.failDetach = false
	restarted, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, req); err == nil {
		t.Fatal("compensation retry error = nil, want compensated terminal outcome")
	}
	loaded, err = sessions.Session(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Participants) != 0 {
		t.Fatalf("participants after compensation retry = %#v, want detached", loaded.Participants)
	}
	entry, err = store.Get(context.Background(), taskID)
	if err != nil || taskStringValue(entry.Metadata["spawn_status"]) != spawnStatusCompensated {
		t.Fatalf("compensation entry = %#v, %v, want terminal compensated", entry, err)
	}
	if runner.spawnCalls != 0 {
		t.Fatalf("spawn calls = %d, want 0 (compensation resume never respawns)", runner.spawnCalls)
	}
}

func TestSubagentSpawnCancelSuccessCannotRollForwardAfterTerminalWriteFailure(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "cancel-terminal"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := &sagaSessionService{Service: base, failParticipant: true}
	store := newSagaTaskStore()
	store.failStatus = spawnStatusCompensated
	runner := &sagaRunner{}
	req := taskapi.SubagentStartRequest{SpawnID: "cancel-terminal", Agent: "helper", Prompt: "review", Role: session.ParticipantRoleSidecar}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, req); err == nil {
		t.Fatal("StartSubagent() error = nil, want participant and terminal write failures")
	}
	sessions.failParticipant = false
	restarted, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, req); err == nil {
		t.Fatal("retry error = nil, want compensated outcome rather than roll-forward")
	}
	taskID, _ := subagentSpawnTaskID(active.SessionRef, req.SpawnID)
	entry, err := store.Get(context.Background(), taskID)
	if err != nil || taskStringValue(entry.Metadata["spawn_status"]) != spawnStatusCompensated {
		t.Fatalf("retry entry = %#v, %v, want compensated", entry, err)
	}
	loaded, err := sessions.Session(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Participants) != 0 || runner.spawnCalls != 1 || runner.cancelCalls != 1 {
		t.Fatalf("participants=%#v spawn/cancel=%d/%d, want no roll-forward", loaded.Participants, runner.spawnCalls, runner.cancelCalls)
	}
}

func TestSubagentSpawnIdentityBindsCompleteSemanticRequest(t *testing.T) {
	t.Parallel()

	changes := []struct {
		name   string
		change func(*taskapi.SubagentStartRequest)
	}{
		{name: "context", change: func(req *taskapi.SubagentStartRequest) {
			req.Context = agent.ContextTransfer{Summary: "different context"}
		}},
		{name: "mode", change: func(req *taskapi.SubagentStartRequest) { req.Mode = "different-mode" }},
		{name: "approval mode", change: func(req *taskapi.SubagentStartRequest) { req.ApprovalMode = "different-approval" }},
		{name: "parent call", change: func(req *taskapi.SubagentStartRequest) { req.ParentCall = "different-call" }},
		{name: "role", change: func(req *taskapi.SubagentStartRequest) { req.Role = session.ParticipantRoleDelegated }},
	}
	for _, change := range changes {
		change := change
		t.Run(change.name, func(t *testing.T) {
			base := memory.NewStore(memory.Config{})
			active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: change.name})
			if err != nil {
				t.Fatal(err)
			}
			store := newSagaTaskStore()
			runner := &sagaRunner{}
			runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
			if err != nil {
				t.Fatal(err)
			}
			req := taskapi.SubagentStartRequest{
				SpawnID: "semantic-request", Agent: "helper", Prompt: "review", Context: agent.ContextTransfer{Summary: "context"},
				Mode: "allow", ApprovalMode: "ask", ParentCall: "call-1", Role: session.ParticipantRoleSidecar,
			}
			if _, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, req); err != nil {
				t.Fatal(err)
			}
			change.change(&req)
			if _, err := runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, req); err == nil || !strings.Contains(err.Error(), "conflicts with durable intent") {
				t.Fatalf("changed request error = %v, want durable identity conflict", err)
			}
			if runner.spawnCalls != 1 {
				t.Fatalf("spawn calls = %d, want 1", runner.spawnCalls)
			}
		})
	}
}

func TestSubagentSpawnRejectsEmptyParticipantAnchorAndCompensates(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "invalid-anchor"})
	if err != nil {
		t.Fatal(err)
	}
	store := newSagaTaskStore()
	runner := &invalidAnchorSagaRunner{}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.tasks.StartSubagent(context.Background(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
		SpawnID: "invalid-anchor", Agent: "helper", Prompt: "review", Role: session.ParticipantRoleSidecar,
	})
	if err == nil || !strings.Contains(err.Error(), "agent_id") {
		t.Fatalf("StartSubagent() error = %v, want invalid agent_id", err)
	}
	loaded, err := base.Session(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Participants) != 0 || runner.cancelCalls != 1 {
		t.Fatalf("participants=%#v cancel calls=%d, want compensated invalid child", loaded.Participants, runner.cancelCalls)
	}
	taskID, _ := subagentSpawnTaskID(active.SessionRef, "invalid-anchor")
	entry, err := store.Get(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	status := taskStringValue(entry.Metadata["spawn_status"])
	if status == spawnStatusSpawned || status == spawnStatusParticipantAttached || status == spawnStatusCommitted {
		t.Fatalf("durable spawn_status = %q after invalid anchor, want compensated path without roll-forward-ready spawned", status)
	}
}

type invalidAnchorSagaRunner struct {
	cancelCalls int
}

func (*invalidAnchorSagaRunner) Spawn(_ context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return delegation.Anchor{TaskID: spawn.TaskID, SessionID: "child-invalid", Agent: req.Agent}, delegation.Result{TaskID: spawn.TaskID, State: delegation.StateCompleted}, nil
}
func (*invalidAnchorSagaRunner) Continue(context.Context, delegation.Anchor, delegation.ContinueRequest) (delegation.Result, error) {
	return delegation.Result{}, nil
}
func (*invalidAnchorSagaRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	return delegation.Result{}, nil
}
func (r *invalidAnchorSagaRunner) Cancel(context.Context, delegation.Anchor) error {
	r.cancelCalls++
	return nil
}

func TestSubagentSpawnRequiresCASBeforeExternalEffect(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
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

func TestSubagentCancelFailsClosedWhenDurableReloadFails(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "reload-outage"})
	if err != nil {
		t.Fatal(err)
	}
	runner := &sagaRunner{}
	storeErr := errors.New("forced task store reload outage")
	store := &getFailingSagaTaskStore{sagaTaskStore: newSagaTaskStore(), err: storeErr}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: base, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	taskID := "cached-subagent"
	runtime.tasks.subagents[taskID] = &subagentTask{
		ref: taskapi.Ref{TaskID: taskID}, sessionRef: active.SessionRef,
		anchor: delegation.Anchor{TaskID: taskID, SessionID: "child"}, runner: runner,
		state: taskapi.StateRunning, running: true,
	}
	_, err = runtime.tasks.Cancel(context.Background(), active.SessionRef, taskapi.ControlRequest{
		TaskID: taskID, Principal: session.ActorKindUser,
	})
	if !errors.Is(err, storeErr) {
		t.Fatalf("Cancel() error = %v, want durable reload outage", err)
	}
	if runner.cancelCalls != 0 {
		t.Fatalf("external Cancel calls = %d, want 0 before durable reload", runner.cancelCalls)
	}
}

func TestConcurrentSubagentSpawnCASCallsExternalSpawnOnce(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
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

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "committed-errors"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := &sagaSessionService{Service: base, commitParticipant: true, commitCanonical: true}
	store := newSagaTaskStore()
	// Put sequence: 1 intent, 2 claim, 3 post_spawn, 4 final_event_persisted, 5 committed.
	// CommittedError recovery is implemented on spawn-phase CAS puts; exercise put 3.
	store.commitOnPut = 3
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
	sessions := store
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
	reopenedSessions := reopenedStore
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
