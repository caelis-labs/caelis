package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type failFromPutTaskStore struct {
	*sagaTaskStore
	mu       sync.Mutex
	puts     int
	failFrom int
	err      error
}

type commandStartProbeRuntime struct {
	*yieldProbeSandboxRuntime
	mu       sync.Mutex
	starts   int
	handle   sandbox.Session
	startErr error
}

func (r *commandStartProbeRuntime) Start(context.Context, sandbox.CommandRequest) (sandbox.Session, error) {
	r.mu.Lock()
	r.starts++
	r.mu.Unlock()
	return r.handle, r.startErr
}

func (r *commandStartProbeRuntime) OpenSession(string) (sandbox.Session, error) {
	return r.handle, nil
}

func (r *commandStartProbeRuntime) OpenSessionRef(sandbox.SessionRef) (sandbox.Session, error) {
	return r.handle, nil
}

func (r *commandStartProbeRuntime) startCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

type commandTerminateProbeSession struct {
	*yieldProbeSandboxSession
	mu    sync.Mutex
	calls int
	err   error
}

func (s *commandTerminateProbeSession) Terminate(context.Context) error {
	s.mu.Lock()
	s.calls++
	err := s.err
	s.mu.Unlock()
	if err == nil && s.statusRunning != nil {
		*s.statusRunning = false
	}
	return err
}

func (s *commandTerminateProbeSession) terminateCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newCommandStartProbe(handle sandbox.Session, startErr error) *commandStartProbeRuntime {
	return &commandStartProbeRuntime{
		yieldProbeSandboxRuntime: &yieldProbeSandboxRuntime{}, handle: handle, startErr: startErr,
	}
}

func TestStartCommandRetryUsesStableParentCallIdentity(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := newSagaTaskStore()
	runtime.tasks.store = store
	running := true
	handle := &commandTerminateProbeSession{yieldProbeSandboxSession: &yieldProbeSandboxSession{statusRunning: &running}}
	backend := newCommandStartProbe(handle, nil)
	req := taskapi.CommandStartRequest{Command: "sleep 60", Workdir: activeSession.CWD, ParentCall: "call-stable", Yield: 0}
	first, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Ref.TaskID == "" || second.Ref.TaskID != first.Ref.TaskID || backend.startCalls() != 1 {
		t.Fatalf("first/second/start calls = %#v/%#v/%d, want stable id and one Start", first.Ref, second.Ref, backend.startCalls())
	}
}

func TestStartCommandRetryRollsForwardDurableIntentAfterClaimFailure(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := newSagaTaskStore()
	store.failOnPut = 2 // intent commits; the first effect claim does not.
	runtime.tasks.store = store
	running := true
	handle := &commandTerminateProbeSession{yieldProbeSandboxSession: &yieldProbeSandboxSession{statusRunning: &running}}
	backend := newCommandStartProbe(handle, nil)
	req := taskapi.CommandStartRequest{Command: "sleep 60", ParentCall: "intent-roll-forward", Yield: 0}

	first, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, req)
	if err == nil || first.Ref.TaskID == "" || first.State != taskapi.StatePrepared || backend.startCalls() != 0 {
		t.Fatalf("first StartCommand() = %#v, %v starts=%d; want durable prepared intent and no effect", first, err, backend.startCalls())
	}
	second, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.Ref.TaskID != first.Ref.TaskID || backend.startCalls() != 1 || !second.Running {
		t.Fatalf("second StartCommand() = %#v starts=%d; want one roll-forward Start", second, backend.startCalls())
	}
}

func TestStartCommandWaitErrorReturnsStableTaskAndPreservesUnknownOutcome(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := newSagaTaskStore()
	runtime.tasks.store = store
	waitErr := errors.New("wait outcome unknown")
	statusErr := errors.New("status outcome unknown")
	handle := &yieldProbeSandboxSession{
		waitErr: waitErr, statusErr: statusErr, terminateErr: errors.New("terminate outcome unknown"),
	}
	backend := newCommandStartProbe(handle, nil)
	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, taskapi.CommandStartRequest{
		Command: "sleep 60", ParentCall: "wait-status-unknown", Yield: 0,
	})
	if !errors.Is(err, waitErr) || !errors.Is(err, statusErr) {
		t.Fatalf("StartCommand() error = %v, want wait and status uncertainty", err)
	}
	if snapshot.Ref.TaskID == "" || snapshot.State != taskapi.StateUnknownOutcome || snapshot.Running {
		t.Fatalf("StartCommand() snapshot = %#v, want terminal durable unknown outcome", snapshot)
	}
	if handle.terminated {
		t.Fatal("transient Wait/Status uncertainty must not issue an unconfirmed Terminate")
	}
	entry, getErr := store.Get(context.Background(), snapshot.Ref.TaskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if entry.State != taskapi.StateUnknownOutcome || entry.Running || taskStringValue(entry.Metadata["command_phase"]) != commandPhaseUnknown {
		t.Fatalf("durable entry = %#v, want retained terminal unknown command", entry)
	}
	runtime.tasks.mu.RLock()
	retained := runtime.tasks.tasks[snapshot.Ref.TaskID]
	runtime.tasks.mu.RUnlock()
	if retained == nil || retained.session == nil {
		t.Fatal("live command handle was discarded while outcome was uncertain")
	}
}

func TestStartCommandTransientWaitErrorReturnsPersistedTaskID(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := newSagaTaskStore()
	runtime.tasks.store = store
	running := true
	waitErr := errors.New("transient wait failure")
	handle := &yieldProbeSandboxSession{waitErr: waitErr, statusRunning: &running}
	backend := newCommandStartProbe(handle, nil)
	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, taskapi.CommandStartRequest{
		Command: "sleep 60", ParentCall: "transient-wait", Yield: 0,
	})
	if !errors.Is(err, waitErr) || snapshot.Ref.TaskID == "" || snapshot.State != taskapi.StateRunning || !snapshot.Running {
		t.Fatalf("StartCommand() = %#v, %v; want stable running snapshot with TaskID", snapshot, err)
	}
}

func TestStartCommandReadOutputErrorReturnsDurableUnknownTaskID(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := newSagaTaskStore()
	runtime.tasks.store = store
	running := false
	readErr := errors.New("terminal output temporarily unavailable")
	handle := &yieldProbeSandboxSession{statusRunning: &running, readErr: readErr}
	backend := newCommandStartProbe(handle, nil)
	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, taskapi.CommandStartRequest{
		Command: "printf done", ParentCall: "read-output-unknown", Yield: 0,
	})
	if !errors.Is(err, readErr) {
		t.Fatalf("StartCommand() error = %v, want %v", err, readErr)
	}
	if snapshot.Ref.TaskID == "" || snapshot.State != taskapi.StateUnknownOutcome || snapshot.Running {
		t.Fatalf("StartCommand() snapshot = %#v, want terminal durable unknown task", snapshot)
	}
	entry, getErr := store.Get(context.Background(), snapshot.Ref.TaskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if entry.State != taskapi.StateUnknownOutcome || entry.Running || taskStringValue(entry.Metadata["command_phase"]) != commandPhaseUnknown {
		t.Fatalf("durable entry = %#v, want terminal output-recovery state", entry)
	}
}

func TestStartCommandPersistsCleanupTerminalAndReturnsTaskID(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := newSagaTaskStore()
	store.failOnPut = 3
	runtime.tasks.store = store
	running := true
	handle := &commandTerminateProbeSession{yieldProbeSandboxSession: &yieldProbeSandboxSession{statusRunning: &running}}
	backend := newCommandStartProbe(handle, nil)
	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, taskapi.CommandStartRequest{
		Command: "sleep 60", ParentCall: "cleanup-terminal",
	})
	if err == nil || snapshot.Ref.TaskID == "" {
		t.Fatalf("StartCommand() = %#v, %v; want reachable cleanup failure", snapshot, err)
	}
	if handle.terminateCalls() != 1 {
		t.Fatalf("Terminate calls = %d, want 1", handle.terminateCalls())
	}
	entry, getErr := store.Get(context.Background(), snapshot.Ref.TaskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if entry.State != taskapi.StateFailed || entry.Running || taskStringValue(entry.Metadata["command_phase"]) != commandPhaseStartFailed {
		t.Fatalf("cleanup entry = %#v, want durable failed terminal", entry)
	}
}

func TestStartCommandCleansUpLiveSessionReturnedWithError(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := newSagaTaskStore()
	runtime.tasks.store = store
	running := true
	handle := &commandTerminateProbeSession{yieldProbeSandboxSession: &yieldProbeSandboxSession{statusRunning: &running}}
	startErr := errors.New("backend reported failure with live session")
	backend := newCommandStartProbe(handle, startErr)
	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, taskapi.CommandStartRequest{
		Command: "sleep 60", ParentCall: "live-error",
	})
	if !errors.Is(err, startErr) || snapshot.Ref.TaskID == "" || handle.terminateCalls() != 1 {
		t.Fatalf("StartCommand() = %#v, %v terminate=%d", snapshot, err, handle.terminateCalls())
	}
	entry, getErr := store.Get(context.Background(), snapshot.Ref.TaskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if entry.State != taskapi.StateFailed || entry.Running {
		t.Fatalf("live-error cleanup entry = %#v", entry)
	}
}

func TestCommandCancelClaimPreventsRepeatedTerminate(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := newSagaTaskStore()
	runtime.tasks.store = store
	running := true
	handle := &commandTerminateProbeSession{yieldProbeSandboxSession: &yieldProbeSandboxSession{statusRunning: &running}}
	backend := newCommandStartProbe(handle, nil)
	started, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, taskapi.CommandStartRequest{
		Command: "sleep 60", ParentCall: "cancel-once",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := taskapi.ControlRequest{TaskID: started.Ref.TaskID, Principal: session.ActorKindUser}
	if _, err := runtime.tasks.Cancel(context.Background(), activeSession.SessionRef, req); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.tasks.Cancel(context.Background(), activeSession.SessionRef, req); err != nil {
		t.Fatal(err)
	}
	if handle.terminateCalls() != 1 {
		t.Fatalf("Terminate calls = %d, want exactly 1", handle.terminateCalls())
	}
}

func TestCommandCancelClaimSurvivesTerminateErrorWithoutRepeatingEffect(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := newSagaTaskStore()
	runtime.tasks.store = store
	running := true
	terminateErr := errors.New("terminate outcome unknown")
	handle := &commandTerminateProbeSession{
		yieldProbeSandboxSession: &yieldProbeSandboxSession{statusRunning: &running}, err: terminateErr,
	}
	backend := newCommandStartProbe(handle, nil)
	started, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, taskapi.CommandStartRequest{
		Command: "sleep 60", ParentCall: "cancel-claim-error",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := taskapi.ControlRequest{TaskID: started.Ref.TaskID, Principal: session.ActorKindUser}
	if _, err := runtime.tasks.Cancel(context.Background(), activeSession.SessionRef, req); !errors.Is(err, terminateErr) {
		t.Fatalf("first Cancel() error = %v, want %v", err, terminateErr)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := runtime.tasks.Cancel(context.Background(), activeSession.SessionRef, req); err != nil {
			t.Fatalf("retry Cancel(%d) error = %v", attempt, err)
		}
	}
	if handle.terminateCalls() != 1 {
		t.Fatalf("Terminate calls = %d, want one claimed external effect", handle.terminateCalls())
	}
	entry, err := store.Get(context.Background(), started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if taskStringValue(entry.Metadata["command_phase"]) != commandPhaseCancelUnknown {
		t.Fatalf("durable command phase = %#v, want uncertain cancel effect retained", entry.Metadata["command_phase"])
	}
}

func TestCommandCancelClaimRollsForwardAfterCacheLoss(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := newSagaTaskStore()
	runtime.tasks.store = store
	running := true
	handle := &commandTerminateProbeSession{yieldProbeSandboxSession: &yieldProbeSandboxSession{statusRunning: &running}}
	backend := newCommandStartProbe(handle, nil)
	started, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, taskapi.CommandStartRequest{
		Command: "sleep 60", ParentCall: "cancel-crash-recovery", Yield: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.Get(context.Background(), started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	entry.State = taskapi.StateUnknownOutcome
	entry.Running = true
	entry.Metadata["state"] = string(taskapi.StateUnknownOutcome)
	entry.Metadata["running"] = true
	entry.Metadata["command_phase"] = commandPhaseCancelClaimed
	entry.Result = map[string]any{"state": string(taskapi.StateUnknownOutcome)}
	if _, err := store.Put(context.Background(), taskapi.PutRequest{Entry: entry, ExpectedRevision: entry.Revision}); err != nil {
		t.Fatal(err)
	}
	runtime.tasks.mu.Lock()
	delete(runtime.tasks.tasks, started.Ref.TaskID) // simulate a process restart/cache loss
	runtime.tasks.mu.Unlock()
	runtime.tasks.registerSandboxRuntime(backend)

	snapshot, err := runtime.tasks.Cancel(context.Background(), activeSession.SessionRef, taskapi.ControlRequest{
		TaskID: started.Ref.TaskID, Principal: session.ActorKindUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if handle.terminateCalls() != 1 || snapshot.Running || snapshot.State == taskapi.StateRunning {
		t.Fatalf("Cancel() = %#v terminate=%d; want claimed effect rolled forward without unknown->running regression", snapshot, handle.terminateCalls())
	}
}

func (s *failFromPutTaskStore) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	s.mu.Lock()
	s.puts++
	put := s.puts
	s.mu.Unlock()
	if put >= s.failFrom {
		return nil, s.err
	}
	return s.sagaTaskStore.Put(ctx, req)
}

func TestStartCommandRetainsLiveHandleWhenInitialPersistenceAndTerminateFail(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	terminateErr := errors.New("forced terminate failure")
	fakeSession := &yieldProbeSandboxSession{terminateErr: terminateErr}
	fake := &yieldProbeSandboxRuntime{session: fakeSession}
	store := newSagaTaskStore()
	store.failOnPut = 3 // durable intent + effect claim succeed; running update fails.
	runtime.tasks.store = store

	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, fake, taskapi.CommandStartRequest{
		Command: "sleep 60",
		Workdir: activeSession.CWD,
		Yield:   0,
	})
	if !errors.Is(err, terminateErr) {
		t.Fatalf("StartCommand() error = %v, want terminate failure", err)
	}
	if snapshot.Ref.TaskID == "" || snapshot.State != taskapi.StateUnknownOutcome || snapshot.Running {
		t.Fatalf("StartCommand() snapshot = %#v, want retained terminal unknown-outcome task", snapshot)
	}
	runtime.tasks.mu.RLock()
	retained := runtime.tasks.tasks[snapshot.Ref.TaskID]
	runtime.tasks.mu.RUnlock()
	if retained == nil {
		t.Fatal("live command handle was removed after cleanup failure")
	}
}

func TestCommandEffectClaimSurvivesTripleFailureAndRestart(t *testing.T) {
	t.Parallel()
	base, activeSession := newTestSessionService(t, "command-effect-claim-restart")
	store := &failFromPutTaskStore{
		sagaTaskStore: newSagaTaskStore(), failFrom: 3,
		err: errors.New("forced command task persistence outage"),
	}
	first, err := New(Config{Sessions: base, AgentFactory: chat.Factory{}, TaskStore: store})
	if err != nil {
		t.Fatal(err)
	}
	terminateErr := errors.New("forced terminate failure")
	fakeSession := &yieldProbeSandboxSession{terminateErr: terminateErr}
	snapshot, err := first.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef,
		&yieldProbeSandboxRuntime{session: fakeSession}, taskapi.CommandStartRequest{Command: "sleep 60"})
	if !errors.Is(err, terminateErr) || snapshot.Ref.TaskID == "" {
		t.Fatalf("StartCommand() = %#v, %v; want reachable task id and terminate failure", snapshot, err)
	}

	restarted, err := New(Config{Sessions: base, AgentFactory: chat.Factory{}, TaskStore: store})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.tasks.Wait(context.Background(), activeSession.SessionRef, taskapi.ControlRequest{
		TaskID: snapshot.Ref.TaskID, Principal: session.ActorKindUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Ref.TaskID != snapshot.Ref.TaskID || recovered.State != taskapi.StateUnknownOutcome {
		t.Fatalf("recovered snapshot = %#v, want durable unknown outcome", recovered)
	}
}

func TestRuntimeCommandToolReturnsUnknownTaskIDOnTripleFailure(t *testing.T) {
	t.Parallel()
	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	store := &failFromPutTaskStore{
		sagaTaskStore: newSagaTaskStore(), failFrom: 3,
		err: errors.New("forced command task persistence outage"),
	}
	runtime.tasks.store = store
	fake := &yieldProbeSandboxRuntime{session: &yieldProbeSandboxSession{terminateErr: errors.New("forced terminate failure")}}
	base := mustRuntimeRunCommandTool(t, fake)
	raw, _ := json.Marshal(map[string]any{"command": "sleep 60", "yield_time_ms": 0})
	result, err := (runtimeCommandTool{base: base, session: activeSession, sessionRef: activeSession.SessionRef, tasks: runtime.tasks}).Call(
		context.Background(), tool.Call{ID: "command-triple-failure", Name: "RUN_COMMAND", Input: raw},
	)
	if err != nil {
		t.Fatalf("Call() error = %v, want canonical error result", err)
	}
	if !result.IsError || len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("Call() result = %#v, want JSON error payload", result)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatal(err)
	}
	if handle, _ := payload["handle"].(string); strings.TrimSpace(handle) == "" {
		t.Fatalf("Call() payload = %#v, want recoverable handle", payload)
	}
}

func TestStartCommandDefersCompletedCommandResultPersistenceToAgentLoop(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	completed := false
	fakeSession := &yieldProbeSandboxSession{
		statusRunning: &completed,
		stdout:        "out\n",
		stderr:        "err\n",
		result: sandbox.CommandResult{
			Stdout:   "out\n",
			Stderr:   "err\n",
			ExitCode: 0,
		},
	}
	fake := &yieldProbeSandboxRuntime{session: fakeSession}
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore

	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, fake, taskapi.CommandStartRequest{
		Command: "echo out; echo err >&2",
		Workdir: activeSession.CWD,
		Yield:   0,
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	if got, _ := snapshot.Result["result"].(string); got != "out\nerr\n" {
		t.Fatalf("snapshot result = %q, want merged terminal summary", got)
	}

	entry, err := taskStore.Get(context.Background(), snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get() error = %v", err)
	}
	if _, exists := entry.Result["result"]; exists {
		t.Fatalf("stored result unexpectedly contains pre-canonical command output: %#v", entry.Result)
	}
	if _, exists := entry.Result["stdout"]; exists {
		t.Fatalf("stored result unexpectedly contains stdout: %#v", entry.Result)
	}
	if _, exists := entry.Result["stderr"]; exists {
		t.Fatalf("stored result unexpectedly contains stderr: %#v", entry.Result)
	}
	if _, exists := entry.Metadata["output_cursor"]; exists {
		t.Fatalf("stored metadata unexpectedly contains pre-canonical output_cursor: %#v", entry.Metadata)
	}
	listed, err := taskStore.ListSession(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("task store ListSession() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed tasks = %d, want 1", len(listed))
	}
	if _, exists := listed[0].Result["stdout"]; exists {
		t.Fatalf("listed result unexpectedly contains stdout: %#v", listed[0].Result)
	}
	if _, exists := listed[0].Result["stderr"]; exists {
		t.Fatalf("listed result unexpectedly contains stderr: %#v", listed[0].Result)
	}
}

func TestTaskRuntimeSyncCanonicalToolResultPersistsCommandResult(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	completed := false
	fakeSession := &yieldProbeSandboxSession{
		statusRunning: &completed,
		stdout:        "raw full output\n",
		result: sandbox.CommandResult{
			Stdout:   "raw full output\n",
			ExitCode: 0,
		},
	}
	fake := &yieldProbeSandboxRuntime{session: fakeSession}
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore

	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, fake, taskapi.CommandStartRequest{
		Command: "printf raw",
		Workdir: activeSession.CWD,
		Yield:   0,
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	canonicalText := "canonical truncated output\n"
	eventTime := time.Unix(123, 0).UTC()
	err = runtime.tasks.syncCanonicalToolResult(context.Background(), activeSession.SessionRef, &session.Event{
		Type: session.EventTypeToolResult,
		Time: eventTime,
		Tool: &session.EventTool{
			Name:   "RUN_COMMAND",
			Status: "completed",
			Output: map[string]any{
				"task_id":   snapshot.Ref.TaskID,
				"state":     string(taskapi.StateCompleted),
				"result":    canonicalText,
				"exit_code": 0,
			},
		},
	})
	if err != nil {
		t.Fatalf("syncCanonicalToolResult() error = %v", err)
	}

	entry, err := taskStore.Get(context.Background(), snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get() error = %v", err)
	}
	if got, _ := entry.Result["result"].(string); got != canonicalText {
		t.Fatalf("stored result = %q, want canonical result", got)
	}
	if got, ok := taskInt64Value(entry.Metadata["output_cursor"]); !ok || got != int64(len([]byte(canonicalText))) {
		t.Fatalf("stored output_cursor = %#v ok=%v, want canonical result byte length", entry.Metadata["output_cursor"], ok)
	}
	if !entry.UpdatedAt.Equal(eventTime) {
		t.Fatalf("stored UpdatedAt = %v, want canonical event time %v", entry.UpdatedAt, eventTime)
	}
}

func TestTaskRuntimeSyncCanonicalToolResultPersistsBatchTaskResults(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore
	for _, id := range []string{"task-a", "task-b", "task-error"} {
		if err := taskStore.Upsert(context.Background(), &taskapi.Entry{
			TaskID:  id,
			Kind:    taskapi.KindCommand,
			Session: activeSession.SessionRef,
			State:   taskapi.StateCompleted,
			Result:  map[string]any{"state": string(taskapi.StateCompleted)},
			Terminal: sandbox.TerminalRef{
				Backend:    sandbox.BackendHost,
				SessionID:  id + "-session",
				TerminalID: id + "-terminal",
			},
		}); err != nil {
			t.Fatalf("Upsert(%s) error = %v", id, err)
		}
	}

	err := runtime.tasks.syncCanonicalToolResult(context.Background(), activeSession.SessionRef, &session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			Name:   "TASK",
			Status: "completed",
			Output: map[string]any{
				"action": "wait",
				"count":  3,
				"tasks": []any{
					map[string]any{"task_id": "task-a", "state": string(taskapi.StateCompleted), "result": "canonical a\n", "exit_code": 0},
					map[string]any{"task_id": "task-b", "state": string(taskapi.StateFailed), "result": "canonical b\n", "exit_code": 1},
					map[string]any{"task_id": "task-error", "error": "not updated"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("syncCanonicalToolResult(batch) error = %v", err)
	}

	for _, tc := range []struct {
		id     string
		result string
		state  taskapi.State
	}{
		{id: "task-a", result: "canonical a\n", state: taskapi.StateCompleted},
		{id: "task-b", result: "canonical b\n", state: taskapi.StateFailed},
	} {
		entry, err := taskStore.Get(context.Background(), tc.id)
		if err != nil {
			t.Fatalf("Get(%s) error = %v", tc.id, err)
		}
		if got, _ := entry.Result["result"].(string); got != tc.result {
			t.Fatalf("Get(%s) result = %q, want %q", tc.id, got, tc.result)
		}
		if entry.State != tc.state {
			t.Fatalf("Get(%s) state = %q, want %q", tc.id, entry.State, tc.state)
		}
	}
	entry, err := taskStore.Get(context.Background(), "task-error")
	if err != nil {
		t.Fatalf("Get(task-error) error = %v", err)
	}
	if _, exists := entry.Result["error"]; exists {
		t.Fatalf("batch item without state unexpectedly overwrote task-error: %#v", entry.Result)
	}
}

func TestLookupCommandBackfillsCanonicalResultFromSessionHistory(t *testing.T) {
	t.Parallel()

	sessions, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore
	entry := &taskapi.Entry{
		TaskID:  "task-backfill",
		Kind:    taskapi.KindCommand,
		Session: activeSession.SessionRef,
		State:   taskapi.StateCompleted,
		Result:  map[string]any{"state": string(taskapi.StateCompleted), "exit_code": 0},
		Terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendHost,
			SessionID:  "term-backfill-session",
			TerminalID: "term-backfill",
		},
	}
	if err := taskStore.Upsert(context.Background(), entry); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type: session.EventTypeToolResult,
			Tool: &session.EventTool{
				Name:   "RUN_COMMAND",
				Status: "completed",
				Output: map[string]any{
					"task_id":   "task-backfill",
					"state":     string(taskapi.StateCompleted),
					"result":    "canonical from history\n",
					"exit_code": 0,
				},
			},
		},
	}); err != nil {
		t.Fatalf("AppendEvent(tool result) error = %v", err)
	}

	task, err := runtime.tasks.lookupCommand(context.Background(), activeSession.SessionRef, "task-backfill")
	if err != nil {
		t.Fatalf("lookupCommand() error = %v", err)
	}
	if got, _ := task.result["result"].(string); got != "canonical from history\n" {
		t.Fatalf("rehydrated task result = %q, want canonical history result", got)
	}
	stored, err := taskStore.Get(context.Background(), "task-backfill")
	if err != nil {
		t.Fatalf("Get(after backfill) error = %v", err)
	}
	if got, _ := stored.Result["result"].(string); got != "canonical from history\n" {
		t.Fatalf("stored result after backfill = %q, want canonical history result", got)
	}
}

func TestLookupCommandCanonicalDoesNotRegressNewerDurableUnknownOutcome(t *testing.T) {
	t.Parallel()

	sessions, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	taskStore := newFileTaskStoreForTest(t)
	runtime.tasks.store = taskStore
	durableTime := time.Now().Add(time.Hour)
	entry := &taskapi.Entry{
		TaskID: "task-monotonic-backfill", Kind: taskapi.KindCommand, Session: activeSession.SessionRef,
		State: taskapi.StateUnknownOutcome, Running: true, UpdatedAt: durableTime,
		Spec:     map[string]any{"command": "sleep 60"},
		Result:   map[string]any{"state": string(taskapi.StateUnknownOutcome)},
		Metadata: map[string]any{"command_phase": commandPhaseEffectClaimed},
	}
	if err := taskStore.Upsert(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	storedBefore, err := taskStore.Get(context.Background(), entry.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{Type: session.EventTypeToolResult, Time: durableTime.Add(time.Hour), Tool: &session.EventTool{
			Name: "RUN_COMMAND", Status: "completed", Output: map[string]any{
				"task_id": entry.TaskID, "state": string(taskapi.StateCompleted), "result": "historical result",
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	task, err := runtime.tasks.lookupCommandCanonical(context.Background(), activeSession.SessionRef, entry.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.state != taskapi.StateUnknownOutcome || task.running {
		t.Fatalf("canonical task state/running = %q/%v, want terminal durable unknown", task.state, task.running)
	}
	storedAfter, err := taskStore.Get(context.Background(), entry.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if storedAfter.Revision != storedBefore.Revision || storedAfter.State != taskapi.StateUnknownOutcome {
		t.Fatalf("durable entry regressed: before=%#v after=%#v", storedBefore, storedAfter)
	}
}

func TestCommandStartRequiresCASStoreBeforeExternalEffect(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	runtime.tasks.store = &upsertOnlySagaStore{base: newSagaTaskStore()}
	running := true
	handle := &commandTerminateProbeSession{yieldProbeSandboxSession: &yieldProbeSandboxSession{statusRunning: &running}}
	backend := newCommandStartProbe(handle, nil)
	_, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, backend, taskapi.CommandStartRequest{
		Command: "sleep 60", ParentCall: "cas-required",
	})
	if err == nil || !strings.Contains(err.Error(), "CASStore") || backend.startCalls() != 0 {
		t.Fatalf("StartCommand() error/start calls = %v/%d, want fail closed before Start", err, backend.startCalls())
	}
}
