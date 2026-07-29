package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

type completionTestTaskStore interface {
	task.Store
	task.CASStore
}

type blockingSubagentFinalSessions struct {
	*sagaSessionService
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockingCompletionIntentTaskStore struct {
	completionTestTaskStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type failingCompletionIntentTaskStore struct {
	completionTestTaskStore
	mu        sync.Mutex
	remaining int
}

func (s *failingCompletionIntentTaskStore) Put(ctx context.Context, req task.PutRequest) (*task.Entry, error) {
	if req.Entry != nil &&
		taskStringValue(req.Entry.Spec[subagentCompletionPhaseKey]) == subagentCompletionPhasePending {
		s.mu.Lock()
		if s.remaining > 0 {
			s.remaining--
			s.mu.Unlock()
			return nil, errors.New("forced transient completion intent failure")
		}
		s.mu.Unlock()
	}
	return s.completionTestTaskStore.Put(ctx, req)
}

func (s *blockingCompletionIntentTaskStore) Put(ctx context.Context, req task.PutRequest) (*task.Entry, error) {
	entry, err := s.completionTestTaskStore.Put(ctx, req)
	if err != nil || req.Entry == nil ||
		taskStringValue(req.Entry.Spec[subagentCompletionPhaseKey]) != subagentCompletionPhasePending {
		return entry, err
	}
	s.once.Do(func() {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	})
	return entry, err
}

func (s *blockingSubagentFinalSessions) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if req.Event != nil &&
		session.EventTypeOf(req.Event) == session.EventTypeAssistant &&
		req.Event.Scope != nil &&
		req.Event.Scope.Participant.Role == session.ParticipantRoleSidecar {
		s.once.Do(func() { close(s.started) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.sagaSessionService.AppendEvent(ctx, req)
}

func TestSubagentApplyResultKeepsFailureDiagnosticTerminalOnly(t *testing.T) {
	task := &subagentTask{result: map[string]any{"error": "previous failure"}}

	task.applyResult(delegation.Result{
		State:   delegation.StateRunning,
		Running: true,
		Error:   "must not leak while running",
	})
	if _, exists := task.result["error"]; exists {
		t.Fatalf("running result retained error: %#v", task.result)
	}

	task.applyResult(delegation.Result{
		State: delegation.StateFailed,
		Error: "current failure",
	})
	if got := taskStringValue(task.result["error"]); got != "current failure" {
		t.Fatalf("failed result error = %q, want current failure", got)
	}
	for _, key := range []string{"result", "final_message", "output_preview"} {
		if _, exists := task.result[key]; exists {
			t.Fatalf("failed result contains %q: %#v", key, task.result)
		}
	}

	task.applyResult(delegation.Result{
		State:         delegation.StateInterrupted,
		OutputPreview: "stale child activity",
		Result:        "must not become a final message",
	})
	if got := taskStringValue(task.result["error"]); got != "subagent interrupted" {
		t.Fatalf("interrupted fallback error = %q, want fixed diagnostic", got)
	}
	for _, key := range []string{"result", "final_message", "output_preview"} {
		if _, exists := task.result[key]; exists {
			t.Fatalf("interrupted result contains %q: %#v", key, task.result)
		}
	}

	task.applyResult(delegation.Result{
		State:  delegation.StateCompleted,
		Error:  "must not leak after completion",
		Result: "done",
	})
	if _, exists := task.result["error"]; exists {
		t.Fatalf("completed result retained stale error: %#v", task.result)
	}
	if got := taskStringValue(task.result["final_message"]); got != "done" {
		t.Fatalf("completed final_message = %q, want done", got)
	}
}

func TestNormalizeSubagentCancelledClearsStaleFailureAndOutputFields(t *testing.T) {
	result := map[string]any{
		"state":          string(task.StateFailed),
		"error":          "previous failure",
		"result":         "previous result",
		"final_message":  "previous final message",
		"output_preview": "previous activity",
		"handle":         "reviewer",
	}

	normalizeSubagentResultForState(&result, task.StateCancelled, "")

	if got := taskStringValue(result["state"]); got != string(task.StateCancelled) {
		t.Fatalf("state = %q, want cancelled", got)
	}
	for _, key := range []string{"error", "result", "final_message", "output_preview"} {
		if _, exists := result[key]; exists {
			t.Fatalf("cancelled result retained %q: %#v", key, result)
		}
	}
	if got := taskStringValue(result["handle"]); got != "reviewer" {
		t.Fatalf("unrelated result metadata = %q, want reviewer", got)
	}
}

func TestSubagentTerminalStreamFrameDoesNotOwnLifecycle(t *testing.T) {
	tests := []struct {
		name       string
		state      task.State
		diagnostic string
	}{
		{name: "failed", state: task.StateFailed, diagnostic: "subagent failed"},
		{name: "interrupted", state: task.StateInterrupted, diagnostic: "subagent interrupted"},
		{name: "unknown outcome", state: task.StateUnknownOutcome, diagnostic: "subagent outcome could not be confirmed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subtask := &subagentTask{
				ref:     task.Ref{TaskID: "task-stream-state"},
				state:   task.StateRunning,
				running: true,
				result: map[string]any{
					"result":         "previous completed turn",
					"final_message":  "previous completed turn",
					"output_preview": "stale activity",
				},
			}
			subtask.applyStreamFrames([]stream.Frame{{
				State:   string(test.state),
				Running: false,
				Closed:  true,
			}})
			snapshot := subtask.snapshot()
			if snapshot.State != task.StateRunning || !snapshot.Running {
				t.Fatalf("snapshot = state %q running %v, want unchanged running lifecycle", snapshot.State, snapshot.Running)
			}
			if got := taskStringValue(snapshot.Result["error"]); got != "" {
				t.Fatalf("snapshot error = %q, want no stream-derived failure", got)
			}
			if len(subtask.streamFrames) != 0 {
				t.Fatalf("stream frames = %#v, want no observer-owned terminal frame", subtask.streamFrames)
			}
		})
	}
}

func TestFailedSubagentDiagnosticSurvivesCanonicalTaskSyncAndRehydrate(t *testing.T) {
	ctx := context.Background()
	const failure = "subagent prompt failed"
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{
			State:   delegation.StateRunning,
			Running: true,
		},
		waitResult: delegation.Result{
			State:         delegation.StateFailed,
			OutputPreview: "child exited before first update",
			Error:         failure,
		},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "review",
		Source: "agent_spawn",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	entry, err := runtime.tasks.store.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get(after producer completion) error = %v", err)
	}
	snapshot := snapshotFromTaskEntry(entry)
	if snapshot.State != task.StateFailed || snapshot.Running {
		t.Fatalf("snapshot = state %q running %v, want terminal failed", snapshot.State, snapshot.Running)
	}
	if runner.waitCalls != 0 {
		t.Fatalf("runner Wait calls = %d, want producer-driven completion", runner.waitCalls)
	}
	if entry.FailureDiagnostic != failure || taskStringValue(entry.Result["error"]) != failure {
		t.Fatalf("durable failure = diagnostic %q result %#v, want typed %q", entry.FailureDiagnostic, entry.Result, failure)
	}
	payload := taskToolPayload(snapshot)
	if got := taskStringValue(payload["error"]); got != failure {
		t.Fatalf("Task payload error = %q, want durable failure", got)
	}
	if _, exists := payload["final_message"]; exists {
		t.Fatalf("Task payload treated failure as final assistant output: %#v", payload)
	}
	err = runtime.tasks.syncCanonicalToolResult(ctx, activeSession.SessionRef, &session.Event{
		Type: session.EventTypeToolResult,
		Meta: taskToolMeta(snapshot),
		Tool: &session.EventTool{
			Name:   "TASK",
			Status: "completed",
			Output: payload,
		},
	})
	if err != nil {
		t.Fatalf("syncCanonicalToolResult() error = %v", err)
	}
	entry, err = runtime.tasks.store.Get(ctx, snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get(after sync) error = %v", err)
	}
	if got := taskStringValue(entry.Result["error"]); got != failure {
		t.Fatalf("stored error = %q, want durable failure", got)
	}
	for _, key := range []string{"result", "final_message", "output_preview"} {
		if _, exists := entry.Result[key]; exists {
			t.Fatalf("stored failure contains %q: %#v", key, entry.Result)
		}
	}

	rehydrated := runtime.tasks.rehydrateSubagentTask(entry).snapshot()
	if got := taskStringValue(rehydrated.Result["error"]); got != failure {
		t.Fatalf("rehydrated error = %q, want durable failure", got)
	}
	if _, exists := rehydrated.Result["final_message"]; exists {
		t.Fatalf("rehydrated failure became final assistant output: %#v", rehydrated.Result)
	}

	// Canonical synchronization remains a pure replacement. Compatibility
	// producers that omit a typed Runtime diagnostic receive the fixed fallback
	// instead of inheriting a previous error through the shared apply path.
	compatibility := task.CloneEntry(entry)
	compatibility.FailureDiagnostic = ""
	applyCanonicalTaskEntry(compatibility, map[string]any{
		"handle": taskPublicHandle(snapshot),
		"state":  string(task.StateFailed),
	}, "completed", time.Now())
	if got := taskStringValue(compatibility.Result["error"]); got != "subagent failed" {
		t.Fatalf("canonical fallback error = %q, want subagent failed", got)
	}
	if compatibility.FailureDiagnostic != "subagent failed" {
		t.Fatalf("canonical typed diagnostic = %q, want subagent failed", compatibility.FailureDiagnostic)
	}
}

func TestSubagentProducerFailureSurvivesFileStoreReopenWithoutTaskObservation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	tasks := sessionfile.NewTaskStore(sessions)
	activeSession, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "completion-reopen", PreferredSessionID: "completion-reopen",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	const failure = "subagent connection closed"
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
		waitResult:  delegation.Result{State: delegation.StateFailed, Error: failure},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: tasks,
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "review",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	if runner.waitCalls != 0 {
		t.Fatalf("runner Wait calls = %d, want producer-driven completion", runner.waitCalls)
	}

	reopenedStore := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	reopenedTasks := sessionfile.NewTaskStore(reopenedStore)
	reopened, err := reopenedTasks.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatalf("reopened Task Get() error = %v", err)
	}
	if reopened.State != task.StateFailed || reopened.Running ||
		reopened.FailureDiagnostic != failure || taskStringValue(reopened.Result["error"]) != failure {
		t.Fatalf("reopened failure = %#v, want typed bounded terminal diagnostic", reopened)
	}
}

func TestSubagentCompletionIntentNeverPersistsRawProducerDiagnostic(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	activeSession, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "completion-secret", PreferredSessionID: "completion-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &blockingCompletionIntentTaskStore{
		completionTestTaskStore: sessionfile.NewTaskStore(sessions),
		started:                 make(chan struct{}),
		release:                 make(chan struct{}),
	}
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: store,
	}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	const rawDiagnostic = "Authorization: Bearer top-secret api_key=private-key at /Users/alice/private"
	completed := make(chan struct{})
	go func() {
		runner.spawnContext.Completion.PublishSubagentCompletion(delegation.Result{
			TaskID: started.Ref.TaskID,
			State:  delegation.StateFailed,
			Error:  rawDiagnostic,
			Result: rawDiagnostic,
		})
		close(completed)
	}()
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("completion did not persist its crash intent")
	}

	reopened := sessionfile.NewTaskStore(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	intent, err := reopened.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	persisted := fmt.Sprint(intent.Spec[subagentCompletionResultKey], intent.Result, intent.FailureDiagnostic)
	for _, secret := range []string{"top-secret", "private-key", "/Users/alice/private"} {
		if strings.Contains(persisted, secret) {
			t.Fatalf("completion intent persisted raw producer secret %q: %s", secret, persisted)
		}
	}
	rawIntent, ok := intent.Spec[subagentCompletionResultKey].(map[string]any)
	if !ok || taskStringValue(rawIntent["error"]) != "subagent failed" {
		t.Fatalf("completion intent diagnostic = %#v, want fixed fallback", rawIntent)
	}

	close(store.release)
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("completion did not finish after intent inspection")
	}
	terminal, err := reopened.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.State != task.StateFailed || terminal.Running ||
		terminal.FailureDiagnostic != "subagent failed" ||
		taskStringValue(terminal.Result["error"]) != "subagent failed" {
		t.Fatalf("terminal diagnostic = %#v, want fixed failed fallback", terminal)
	}
}

func TestSubagentCompletionRetryUsesOneRuntimeCoordinatorForMultipleTasks(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	first, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstSink := runner.spawnContext.Completion
	second, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "second",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondSink := runner.spawnContext.Completion
	baseStore := runtime.tasks.store
	runtime.tasks.store = &failingCompletionIntentTaskStore{
		completionTestTaskStore: baseStore.(completionTestTaskStore),
		remaining:               8,
	}

	firstSink.PublishSubagentCompletion(delegation.Result{
		TaskID: first.Ref.TaskID, State: delegation.StateCompleted, Result: "first done",
	})
	secondSink.PublishSubagentCompletion(delegation.Result{
		TaskID: second.Ref.TaskID, State: delegation.StateCompleted, Result: "second done",
	})
	runtime.tasks.mu.RLock()
	workerStarted := runtime.tasks.completionWorker
	pendingStarted := len(runtime.tasks.completions)
	runtime.tasks.mu.RUnlock()
	if !workerStarted || pendingStarted != 2 {
		t.Fatalf("completion coordinator = worker %v pending %d, want one active coordinator owning two pending tasks", workerStarted, pendingStarted)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		firstEntry, firstErr := baseStore.Get(ctx, first.Ref.TaskID)
		secondEntry, secondErr := baseStore.Get(ctx, second.Ref.TaskID)
		runtime.tasks.mu.RLock()
		pending := len(runtime.tasks.completions)
		worker := runtime.tasks.completionWorker
		runtime.tasks.mu.RUnlock()
		if firstErr == nil && secondErr == nil &&
			firstEntry.State == task.StateCompleted && !firstEntry.Running &&
			secondEntry.State == task.StateCompleted && !secondEntry.Running &&
			pending == 0 && !worker {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("completion coordinator did not drain: first=%#v err=%v second=%#v err=%v pending=%d worker=%v",
				firstEntry, firstErr, secondEntry, secondErr, pending, worker)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSubagentProducerCompletionOutlivesParentRuntimeLease(t *testing.T) {
	tests := []struct {
		name             string
		acquireNextLease bool
	}{
		{name: "released parent lease"},
		{name: "new parent lease active", acquireNextLease: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			sessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
				AppName: "caelis", UserID: "completion-lease", PreferredSessionID: "completion-lease",
			})
			if err != nil {
				t.Fatalf("StartSession() error = %v", err)
			}
			runner := &recordingSubagentRunner{
				spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
			}
			runtime, err := New(testConfigWithACPForwarder(Config{
				Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner,
				TaskStore: sessionfile.NewTaskStore(sessions),
			}))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			leases, ok := runtime.sessions.(session.SessionLeaseService)
			if !ok {
				t.Fatal("Session service does not support execution leases")
			}
			parentLease, err := leases.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
				SessionRef: activeSession.SessionRef,
				OwnerID:    "parent-turn",
				TTL:        time.Minute,
			})
			if err != nil {
				t.Fatalf("AcquireSessionLease(parent) error = %v", err)
			}
			parentCtx := session.ContextWithRuntimeLease(context.Background(), parentLease)
			started, err := runtime.tasks.StartSubagent(
				parentCtx,
				activeSession,
				activeSession.SessionRef,
				runner,
				task.SubagentStartRequest{
					Agent: "helper", Prompt: "review", Source: "slash_agent", Role: session.ParticipantRoleSidecar,
				},
			)
			if err != nil {
				t.Fatalf("StartSubagent() error = %v", err)
			}
			sink, ok := runner.spawnContext.Completion.(subagentCompletionSink)
			if !ok {
				t.Fatalf("completion sink = %T, want Runtime-owned sink", runner.spawnContext.Completion)
			}
			guard := session.RuntimeMutationGuard(sink.ctx)
			if guard.Authority != session.MutationAuthorityControl ||
				guard.Purpose != session.ControlMutationPurposeSubagentCompletion ||
				guard.LeaseID != "" || guard.FencingToken != 0 {
				t.Fatalf("completion guard = %#v, want unfenced explicit subagent_completion authority", guard)
			}
			if err := leases.ReleaseSessionLease(context.Background(), session.ReleaseSessionLeaseRequest{
				SessionRef:            activeSession.SessionRef,
				LeaseID:               parentLease.LeaseID,
				OwnerID:               parentLease.OwnerID,
				ExpectedLeaseRevision: parentLease.Revision,
			}); err != nil {
				t.Fatalf("ReleaseSessionLease(parent) error = %v", err)
			}
			if test.acquireNextLease {
				nextLease, acquireErr := leases.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
					SessionRef: activeSession.SessionRef,
					OwnerID:    "next-turn",
					TTL:        time.Minute,
				})
				if acquireErr != nil {
					t.Fatalf("AcquireSessionLease(next) error = %v", acquireErr)
				}
				t.Cleanup(func() {
					_ = leases.ReleaseSessionLease(context.Background(), session.ReleaseSessionLeaseRequest{
						SessionRef:            activeSession.SessionRef,
						LeaseID:               nextLease.LeaseID,
						OwnerID:               nextLease.OwnerID,
						ExpectedLeaseRevision: nextLease.Revision,
					})
				})
			}

			runner.spawnContext.Completion.PublishSubagentCompletion(delegation.Result{
				TaskID: started.Ref.TaskID,
				State:  delegation.StateCompleted,
				Result: "done after parent turn",
			})
			reopenedSessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			entry, err := sessionfile.NewTaskStore(reopenedSessions).Get(context.Background(), started.Ref.TaskID)
			if err != nil {
				t.Fatalf("reopened TaskStore.Get() error = %v", err)
			}
			rehydrated := runtime.tasks.rehydrateSubagentTask(entry).snapshot()
			if entry.Running || entry.State != task.StateCompleted ||
				taskStringValue(rehydrated.Result["final_message"]) != "done after parent turn" {
				t.Fatalf("durable completion = %#v, want terminal result after parent lease changed", entry)
			}
			loaded, err := reopenedSessions.LoadSession(context.Background(), session.LoadSessionRequest{
				SessionRef: activeSession.SessionRef,
			})
			if err != nil {
				t.Fatalf("reopened LoadSession() error = %v", err)
			}
			foundFinal := false
			for _, event := range loaded.Events {
				if event != nil &&
					session.EventTypeOf(event) == session.EventTypeAssistant &&
					session.EventText(event) == "done after parent turn" {
					foundFinal = true
					break
				}
			}
			if !foundFinal {
				t.Fatal("completion did not persist Side final after parent lease changed")
			}
		})
	}
}

func TestSubagentCompletionRemainsRunningUntilSideFinalCommit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	base := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	activeSession, err := base.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "completion-visibility", PreferredSessionID: "completion-visibility",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	sessions := &blockingSubagentFinalSessions{
		sagaSessionService: &sagaSessionService{Service: base},
		started:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: runner,
		TaskStore: sessionfile.NewTaskStore(base),
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "review", Source: "slash_agent", Role: session.ParticipantRoleSidecar,
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	baseline, err := runtime.tasks.Read(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindUser,
	})
	if err != nil {
		t.Fatalf("Read(baseline) error = %v", err)
	}

	completed := make(chan struct{})
	go func() {
		runner.spawnContext.Completion.PublishSubagentCompletion(delegation.Result{
			TaskID: started.Ref.TaskID, State: delegation.StateCompleted, Result: "committed side final",
		})
		close(completed)
	}()
	select {
	case <-sessions.started:
	case <-time.After(2 * time.Second):
		t.Fatal("completion did not reach blocked Side final append")
	}
	select {
	case <-completed:
		t.Fatal("completion returned before Side final append was released")
	default:
	}

	observed, err := runtime.tasks.Read(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindUser,
	})
	if err != nil {
		t.Fatalf("Read(blocked completion) error = %v", err)
	}
	if !observed.Running || observed.State != task.StateRunning {
		t.Fatalf("Read(blocked completion) = %#v, want live running lifecycle", observed)
	}
	if observed.Revision != baseline.Revision || !reflect.DeepEqual(observed.Lease, baseline.Lease) {
		t.Fatalf("Read(blocked completion) revision/lease = %d/%#v, want baseline %d/%#v", observed.Revision, observed.Lease, baseline.Revision, baseline.Lease)
	}
	durableWhileBlocked, err := runtime.tasks.store.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatalf("TaskStore.Get(blocked completion) error = %v", err)
	}
	if durableWhileBlocked.Revision <= baseline.Revision ||
		durableWhileBlocked.State != task.StateRunning ||
		!durableWhileBlocked.Running ||
		taskStringValue(durableWhileBlocked.Metadata[subagentCompletionPhaseKey]) != subagentCompletionPhasePending {
		t.Fatalf("durable blocked completion = %#v, want recoverable Running completion intent", durableWhileBlocked)
	}
	waited, err := runtime.tasks.Wait(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindUser, Yield: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait(blocked completion) error = %v", err)
	}
	if !waited.Running || waited.State != task.StateRunning {
		t.Fatalf("Wait(blocked completion) = %#v, want live running lifecycle", waited)
	}
	streamed, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Streams.Read(blocked completion) error = %v", err)
	}
	if !streamed.Running || streamed.State != string(task.StateRunning) {
		t.Fatalf("Streams.Read(blocked completion) = %#v, want live running lifecycle", streamed)
	}

	// Simulate a process restart at the exact crash window: the original
	// producer is blocked after writing the Running completion intent but
	// before Side final. Recovery must idempotently roll the intent through
	// Side final/checkpoint and the final terminal CAS.
	reopenedSessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	restarted, err := New(testConfigWithACPForwarder(Config{
		Sessions: reopenedSessions, AgentFactory: chat.Factory{},
		Subagents: &recordingSubagentRunner{}, TaskStore: sessionfile.NewTaskStore(reopenedSessions),
	}))
	if err != nil {
		t.Fatalf("New(restarted) error = %v", err)
	}
	if err := restarted.recoverRuntimeState(ctx, activeSession.SessionRef); err != nil {
		t.Fatalf("recoverRuntimeState() error = %v", err)
	}
	recovered, err := restarted.tasks.store.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatalf("TaskStore.Get(recovered) error = %v", err)
	}
	if recovered.Running || recovered.State != task.StateCompleted ||
		taskStringValue(recovered.Metadata[subagentCompletionPhaseKey]) != "" {
		t.Fatalf("recovered completion = %#v, want terminal with cleared intent", recovered)
	}

	close(sessions.release)
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("completion did not finish after Side final append release")
	}
	terminal, err := runtime.tasks.Read(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindUser,
	})
	if err != nil {
		t.Fatalf("Read(terminal) error = %v", err)
	}
	durable, err := runtime.tasks.store.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatalf("TaskStore.Get(terminal) error = %v", err)
	}
	if terminal.Running || terminal.State != task.StateCompleted ||
		terminal.Revision != durable.Revision || durable.State != task.StateCompleted {
		t.Fatalf("published/durable terminal = %#v / %#v, want same committed revision", terminal, durable)
	}
	loaded, err := reopenedSessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	foundFinal := false
	for _, event := range loaded.Events {
		if event != nil &&
			session.EventTypeOf(event) == session.EventTypeAssistant &&
			session.EventText(event) == "committed side final" {
			foundFinal = true
			break
		}
	}
	if !foundFinal {
		t.Fatal("completion published terminal without durable Side final")
	}
}

func TestSubagentProducerCompletionIsMonotonicAndTerminalFramedOnce(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "review",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}

	runner.spawnContext.Completion.PublishSubagentCompletion(delegation.Result{
		TaskID: started.Ref.TaskID,
		State:  delegation.StateCompleted,
		Result: "exact final",
	})
	first, err := runtime.tasks.store.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatalf("Get(first completion) error = %v", err)
	}
	runner.spawnContext.Completion.PublishSubagentCompletion(delegation.Result{
		TaskID: started.Ref.TaskID,
		State:  delegation.StateFailed,
		Error:  "subagent prompt failed",
	})
	second, err := runtime.tasks.store.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatalf("Get(duplicate completion) error = %v", err)
	}
	if second.Revision != first.Revision || second.State != task.StateCompleted {
		t.Fatalf("duplicate completion changed durable terminal: first=%#v second=%#v", first, second)
	}
	if got := taskStringValue(runtime.tasks.rehydrateSubagentTask(second).snapshot().Result["final_message"]); got != "exact final" {
		t.Fatalf("rehydrated final_message = %q, want first terminal result", got)
	}

	streamSnapshot, err := runtime.Streams().Read(ctx, stream.ReadRequest{
		Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: started.Ref.TaskID},
	})
	if err != nil {
		t.Fatalf("Streams().Read() error = %v", err)
	}
	closed := 0
	for _, frame := range streamSnapshot.Frames {
		if frame.Closed {
			closed++
		}
	}
	if closed != 1 || streamSnapshot.State != string(task.StateCompleted) || streamSnapshot.FinalText != "exact final" {
		t.Fatalf("terminal stream = %#v, want one completed close with exact final", streamSnapshot)
	}
}

func TestSubagentWaitWakesFromProducerCompletionWithoutPollingRunner(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{State: delegation.StateRunning, Running: true},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "review",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}

	type waitOutcome struct {
		snapshot task.Snapshot
		err      error
	}
	waited := make(chan waitOutcome, 1)
	go func() {
		snapshot, waitErr := runtime.tasks.Wait(ctx, activeSession.SessionRef, task.ControlRequest{
			TaskID: started.Ref.TaskID, Principal: session.ActorKindTool, Yield: time.Second,
		})
		waited <- waitOutcome{snapshot: snapshot, err: waitErr}
	}()
	runner.spawnContext.Completion.PublishSubagentCompletion(delegation.Result{
		TaskID: started.Ref.TaskID,
		State:  delegation.StateCompleted,
		Result: "done",
	})

	select {
	case outcome := <-waited:
		if outcome.err != nil || outcome.snapshot.State != task.StateCompleted || outcome.snapshot.Running {
			t.Fatalf("Wait() = %#v, %v, want producer-completed snapshot", outcome.snapshot, outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() did not wake from producer completion")
	}
	if runner.waitCalls != 0 {
		t.Fatalf("runner Wait calls = %d, want zero", runner.waitCalls)
	}
}

func TestSubagentStaleTurnCompletionCannotTerminateContinuation(t *testing.T) {
	ctx := context.Background()
	runner := &recordingSubagentRunner{
		spawnResult:    delegation.Result{State: delegation.StateRunning, Running: true},
		continueResult: delegation.Result{State: delegation.StateRunning, Running: true},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent: "helper", Prompt: "first",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	oldTurn := runner.spawnContext.Completion
	oldTurn.PublishSubagentCompletion(delegation.Result{
		TaskID: started.Ref.TaskID, State: delegation.StateCompleted, Result: "first done",
	})
	if _, err := runtime.tasks.Write(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindTool, Input: "continue",
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	oldTurn.PublishSubagentCompletion(delegation.Result{
		TaskID: started.Ref.TaskID, State: delegation.StateFailed, Error: "subagent prompt failed",
	})
	running := runtime.tasks.subagents[started.Ref.TaskID].snapshot()
	if !running.Running || running.State != task.StateRunning || taskTurnSeqFromSpec(running.Metadata) != 2 {
		t.Fatalf("stale completion changed continuation = %#v", running)
	}

	runner.continueCompletion.PublishSubagentCompletion(delegation.Result{
		TaskID: started.Ref.TaskID, State: delegation.StateCompleted, Result: "second done",
	})
	completed := runtime.tasks.subagents[started.Ref.TaskID].snapshot()
	if completed.Running || completed.State != task.StateCompleted ||
		taskStringValue(completed.Result["final_message"]) != "second done" {
		t.Fatalf("current completion = %#v, want second turn terminal", completed)
	}
}

func TestSubagentTaskPayloadOwnsFailureDiagnostic(t *testing.T) {
	const rawLegacyError = "Bearer direct-secret api_key=direct-key /Users/alice/private"
	result := map[string]any{
		"output_preview": "stale activity",
		"final_message":  "previous completed turn",
		"error":          rawLegacyError,
	}
	normalizeSubagentResultForState(&result, task.StateUnknownOutcome, "")
	payload := taskToolPayload(task.Snapshot{
		Handle:  "reviewer",
		Kind:    task.KindSubagent,
		State:   task.StateUnknownOutcome,
		Running: true,
		Result:  result,
	})
	if got := taskStringValue(payload["error"]); got != "subagent outcome could not be confirmed" {
		t.Fatalf("payload error = %q, want fixed unknown-outcome diagnostic", got)
	}
	for _, key := range []string{"output_preview", "final_message"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("failure payload contains %q: %#v", key, payload)
		}
	}
	if exposed := fmt.Sprint(payload); strings.Contains(exposed, "direct-secret") ||
		strings.Contains(exposed, "direct-key") || strings.Contains(exposed, "/Users/alice") {
		t.Fatalf("failure payload exposed unmarked legacy error: %s", exposed)
	}
}

func TestLegacySubagentFailureErrorIsNotPromotedAcrossRehydrate(t *testing.T) {
	tests := []struct {
		name       string
		state      task.State
		rawError   string
		secrets    []string
		diagnostic string
	}{
		{
			name:       "failed bearer",
			state:      task.StateFailed,
			rawError:   "request failed: Authorization: Bearer legacy-token at /Users/alice/work",
			secrets:    []string{"legacy-token", "/Users/alice"},
			diagnostic: "subagent failed",
		},
		{
			name:       "interrupted api key",
			state:      task.StateInterrupted,
			rawError:   "transport interrupted: api_key=legacy-api-key at /private/tmp/child.sock",
			secrets:    []string{"legacy-api-key", "/private/tmp/child.sock"},
			diagnostic: "subagent interrupted",
		},
		{
			name:       "unknown outcome path",
			state:      task.StateUnknownOutcome,
			rawError:   "unknown effect: sk-legacy-secret in /home/service/.config",
			secrets:    []string{"sk-legacy-secret", "/home/service/.config"},
			diagnostic: "subagent outcome could not be confirmed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			runtime, activeSession := newSubagentTaskTestRuntime(t, &recordingSubagentRunner{})
			taskID := "legacy-" + strings.ReplaceAll(test.name, " ", "-")
			entry := &task.Entry{
				TaskID:  taskID,
				Handle:  taskID,
				Kind:    task.KindSubagent,
				Session: activeSession.SessionRef,
				State:   test.state,
				Spec: map[string]any{
					"agent":      "helper",
					"handle":     taskID,
					"session_id": "child-" + taskID,
					"agent_id":   "agent-" + taskID,
				},
				Result: map[string]any{
					"state":         string(test.state),
					"error":         test.rawError,
					"result":        test.rawError,
					"final_message": test.rawError,
				},
			}
			if err := runtime.tasks.store.Upsert(ctx, entry); err != nil {
				t.Fatalf("store Upsert() error = %v", err)
			}
			stored, err := runtime.tasks.store.Get(ctx, taskID)
			if err != nil || taskStringValue(stored.Result["error"]) != test.rawError {
				t.Fatalf("legacy store round-trip = %#v, %v; want raw fixture retained before trust boundary", stored, err)
			}

			subtask, err := runtime.tasks.lookupSubagent(ctx, activeSession.SessionRef, taskID)
			if err != nil {
				t.Fatalf("lookupSubagent() error = %v", err)
			}
			snapshot := subtask.snapshot()
			payload := taskToolPayload(snapshot)
			if got := taskStringValue(snapshot.Result["error"]); got != test.diagnostic {
				t.Fatalf("rehydrated error = %q, want %q", got, test.diagnostic)
			}
			if got := taskStringValue(payload["error"]); got != test.diagnostic {
				t.Fatalf("Task payload error = %q, want %q", got, test.diagnostic)
			}
			for _, key := range []string{"result", "final_message", "output_preview"} {
				if _, exists := snapshot.Result[key]; exists {
					t.Fatalf("rehydrated failure retained %q: %#v", key, snapshot.Result)
				}
				if _, exists := payload[key]; exists {
					t.Fatalf("Task payload retained %q: %#v", key, payload)
				}
			}
			exposed := fmt.Sprint(snapshot.Result, payload)
			for _, secret := range test.secrets {
				if strings.Contains(exposed, secret) {
					t.Fatalf("legacy secret %q crossed rehydrate boundary: %s", secret, exposed)
				}
			}
		})
	}
}

func TestLegacyNonFailureSubagentErrorIsDroppedFromRehydratePayloadAndStream(t *testing.T) {
	tests := []struct {
		name    string
		state   task.State
		running bool
		result  map[string]any
	}{
		{
			name:    "running",
			state:   task.StateRunning,
			running: true,
			result:  map[string]any{"output_preview": "working"},
		},
		{
			name:   "completed",
			state:  task.StateCompleted,
			result: map[string]any{"result": "completed answer", "final_message": "completed answer"},
		},
		{
			name:   "cancelled",
			state:  task.StateCancelled,
			result: map[string]any{"result": "cancelled", "final_message": "cancelled"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			runtime, activeSession := newSubagentTaskTestRuntime(t, &recordingSubagentRunner{})
			taskID := "legacy-nonfailure-" + test.name
			const rawError = "Authorization: Bearer nonfailure-secret api_key=nonfailure-key at /Users/alice/private"
			result := session.CloneState(test.result)
			result["state"] = string(test.state)
			result["error"] = rawError

			directPayload := taskToolPayload(task.Snapshot{
				Handle: taskID, Kind: task.KindSubagent, State: test.state, Running: test.running, Result: result,
			})
			if exposed := fmt.Sprint(directPayload); strings.Contains(exposed, "nonfailure-secret") ||
				strings.Contains(exposed, "nonfailure-key") || strings.Contains(exposed, "/Users/alice") {
				t.Fatalf("direct Task payload exposed non-failure error: %s", exposed)
			}
			if _, exists := directPayload["error"]; exists {
				t.Fatalf("direct Task payload retained non-failure error: %#v", directPayload)
			}

			if err := runtime.tasks.store.Upsert(ctx, &task.Entry{
				TaskID:  taskID,
				Handle:  taskID,
				Kind:    task.KindSubagent,
				Session: activeSession.SessionRef,
				State:   test.state,
				Running: test.running,
				Spec: map[string]any{
					"agent":      "helper",
					"handle":     taskID,
					"session_id": "child-" + taskID,
					"agent_id":   "agent-" + taskID,
				},
				Result: result,
			}); err != nil {
				t.Fatalf("store Upsert() error = %v", err)
			}
			subtask, err := runtime.tasks.lookupSubagent(ctx, activeSession.SessionRef, taskID)
			if err != nil {
				t.Fatalf("lookupSubagent() error = %v", err)
			}
			snapshot := subtask.snapshot()
			if _, exists := snapshot.Result["error"]; exists {
				t.Fatalf("rehydrated non-failure retained error: %#v", snapshot.Result)
			}
			payload := taskToolPayload(snapshot)
			streamSnapshot, err := runtime.Streams().Read(ctx, stream.ReadRequest{
				Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: taskID},
			})
			if err != nil {
				t.Fatalf("Streams().Read() error = %v", err)
			}
			exposed := fmt.Sprint(snapshot.Result, payload, streamSnapshot.FinalText)
			for _, secret := range []string{"nonfailure-secret", "nonfailure-key", "/Users/alice"} {
				if strings.Contains(exposed, secret) {
					t.Fatalf("legacy non-failure secret %q crossed read boundary: %s", secret, exposed)
				}
			}
			if _, exists := payload["error"]; exists {
				t.Fatalf("Task payload retained non-failure error: %#v", payload)
			}
		})
	}
}

func TestCanonicalSubagentFailureDoesNotPromoteUntypedDiagnostic(t *testing.T) {
	for _, test := range []struct {
		name     string
		backfill bool
	}{
		{name: "sync"},
		{name: "backfill", backfill: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			runtime, activeSession := newSubagentTaskTestRuntime(t, &recordingSubagentRunner{})
			taskID := "compat-failed-" + test.name
			const rawError = "Authorization: Bearer canonical-secret api_key=canonical-key at /Users/alice/private"
			if err := runtime.tasks.store.Upsert(ctx, &task.Entry{
				TaskID:    taskID,
				Handle:    taskID,
				Kind:      task.KindSubagent,
				Session:   activeSession.SessionRef,
				State:     task.StateFailed,
				UpdatedAt: time.Now().Add(-time.Minute),
				Result:    map[string]any{"state": string(task.StateFailed), "error": rawError},
			}); err != nil {
				t.Fatalf("store Upsert() error = %v", err)
			}
			event := &session.Event{
				Type: session.EventTypeToolResult,
				Tool: &session.EventTool{
					Name:   "TASK",
					Status: "completed",
					Output: map[string]any{
						"task_id": taskID,
						"handle":  taskID,
						"state":   string(task.StateFailed),
						"error":   rawError,
					},
				},
			}
			if test.backfill {
				if _, err := runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{
					SessionRef: activeSession.SessionRef,
					Event:      event,
				}); err != nil {
					t.Fatalf("AppendEvent() error = %v", err)
				}
				if _, err := runtime.tasks.lookupSubagent(ctx, activeSession.SessionRef, taskID); err != nil {
					t.Fatalf("lookupSubagent() error = %v", err)
				}
			} else if err := runtime.tasks.syncCanonicalToolResult(ctx, activeSession.SessionRef, event); err != nil {
				t.Fatalf("syncCanonicalToolResult() error = %v", err)
			}
			entry, err := runtime.tasks.store.Get(ctx, taskID)
			if err != nil {
				t.Fatalf("store Get() error = %v", err)
			}
			if got := taskStringValue(entry.Result["error"]); got != "subagent failed" {
				t.Fatalf("canonical error = %q, want fixed fallback", got)
			}
			if entry.FailureDiagnostic != "subagent failed" {
				t.Fatalf("typed diagnostic = %q, want fixed fallback", entry.FailureDiagnostic)
			}
			exposed := fmt.Sprint(entry.Result, entry.FailureDiagnostic)
			for _, secret := range []string{"canonical-secret", "canonical-key", "/Users/alice"} {
				if strings.Contains(exposed, secret) {
					t.Fatalf("canonical %s promoted untyped secret %q: %s", test.name, secret, exposed)
				}
			}
		})
	}
}

func TestCanonicalNonFailureSubagentDropsLegacyError(t *testing.T) {
	tests := []struct {
		name     string
		state    task.State
		backfill bool
	}{
		{name: "sync completed", state: task.StateCompleted},
		{name: "sync cancelled", state: task.StateCancelled},
		{name: "backfill completed", state: task.StateCompleted, backfill: true},
		{name: "backfill cancelled", state: task.StateCancelled, backfill: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			runtime, activeSession := newSubagentTaskTestRuntime(t, &recordingSubagentRunner{})
			taskID := "legacy-canonical-" + strings.ReplaceAll(test.name, " ", "-")
			const (
				rawError    = "Authorization: Bearer canonical-secret api_key=canonical-key at /Users/alice/private"
				finalOutput = "safe canonical output"
			)
			if err := runtime.tasks.store.Upsert(ctx, &task.Entry{
				TaskID:  taskID,
				Handle:  taskID,
				Kind:    task.KindSubagent,
				Session: activeSession.SessionRef,
				State:   test.state,
				Spec: map[string]any{
					"agent":      "helper",
					"handle":     taskID,
					"session_id": "child-" + taskID,
					"agent_id":   "agent-" + taskID,
				},
				Result: map[string]any{
					"state": string(test.state),
					"error": rawError,
				},
				UpdatedAt: time.Now().Add(-time.Minute),
			}); err != nil {
				t.Fatalf("store Upsert() error = %v", err)
			}

			event := &session.Event{
				Type: session.EventTypeToolResult,
				Tool: &session.EventTool{
					Name:   "TASK",
					Status: "completed",
					Output: map[string]any{
						"task_id":       taskID,
						"handle":        taskID,
						"state":         string(test.state),
						"error":         rawError,
						"final_message": finalOutput,
					},
				},
			}
			if test.backfill {
				if _, err := runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{
					SessionRef: activeSession.SessionRef,
					Event:      event,
				}); err != nil {
					t.Fatalf("AppendEvent() error = %v", err)
				}
			} else if err := runtime.tasks.syncCanonicalToolResult(ctx, activeSession.SessionRef, event); err != nil {
				t.Fatalf("syncCanonicalToolResult() error = %v", err)
			}

			subtask, err := runtime.tasks.lookupSubagent(ctx, activeSession.SessionRef, taskID)
			if err != nil {
				t.Fatalf("lookupSubagent() error = %v", err)
			}
			stored, err := runtime.tasks.store.Get(ctx, taskID)
			if err != nil {
				t.Fatalf("store Get() error = %v", err)
			}
			snapshot := subtask.snapshot()
			payload := taskToolPayload(snapshot)
			for label, result := range map[string]map[string]any{
				"stored":   stored.Result,
				"snapshot": snapshot.Result,
				"payload":  payload,
			} {
				if _, exists := result["error"]; exists {
					t.Fatalf("%s retained legacy non-failure error: %#v", label, result)
				}
			}
			exposed := fmt.Sprint(stored.Result, snapshot.Result, payload)
			for _, secret := range []string{"canonical-secret", "canonical-key", "/Users/alice"} {
				if strings.Contains(exposed, secret) {
					t.Fatalf("canonical %s exposed %q: %s", test.name, secret, exposed)
				}
			}
		})
	}
}

func TestCanonicalSubagentBatchNormalizesEachFailureIndependently(t *testing.T) {
	failedResult := map[string]any{}
	unknownResult := map[string]any{}
	normalizeSubagentResultForState(&failedResult, task.StateFailed, "subagent prompt failed")
	normalizeSubagentResultForState(
		&unknownResult,
		task.StateUnknownOutcome,
		subagentContinueUnknownDiagnostic,
	)
	items := []taskBatchControlItem{
		{
			Handle: "failed-child",
			OK:     true,
			Snapshot: task.Snapshot{
				Handle: "failed-child", Kind: task.KindSubagent,
				State: task.StateFailed, Result: failedResult,
			},
		},
		{
			Handle: "unknown-child",
			OK:     true,
			Snapshot: task.Snapshot{
				Handle: "unknown-child", Kind: task.KindSubagent,
				State: task.StateUnknownOutcome, Result: unknownResult,
			},
		},
	}
	payload := taskBatchControlPayload(items, "wait", 0)
	outputs, ok := canonicalTaskBatchOutputs(payload["tasks"])
	if !ok || len(outputs) != 2 {
		t.Fatalf("canonical batch outputs = %#v, want two items", outputs)
	}
	got := []string{
		taskStringValue(canonicalSubagentTaskOutput(
			outputs[0], "completed", task.StateFailed, "subagent prompt failed",
		)["error"]),
		taskStringValue(canonicalSubagentTaskOutput(
			outputs[1], "completed", task.StateUnknownOutcome, subagentContinueUnknownDiagnostic,
		)["error"]),
	}
	want := []string{"subagent prompt failed", subagentContinueUnknownDiagnostic}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("canonical batch diagnostic[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
