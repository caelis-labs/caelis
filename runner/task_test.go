package runner

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/sandbox/host"
	"github.com/OnslaughtSnail/caelis/tool"
)

// ─── Task manager tests ──────────────────────────────────────────────

type mockBackend struct {
	result sandbox.CommandResult
	err    error
}

func (b *mockBackend) Name() string { return "mock" }
func (b *mockBackend) Describe(_ context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{Name: "mock"}, nil
}
func (b *mockBackend) Run(_ context.Context, _ sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return b.result, b.err
}
func (b *mockBackend) FileSystem(_ context.Context, _ sandbox.Constraints) (sandbox.FileSystem, error) {
	return nil, nil
}
func (b *mockBackend) Status(_ context.Context) (sandbox.Status, error) {
	return sandbox.Status{Running: true}, nil
}
func (b *mockBackend) Close() error { return nil }

func TestTaskManager_StartAndWait(t *testing.T) {
	backend := &mockBackend{
		result: sandbox.CommandResult{Stdout: []byte("hello"), ExitCode: 0},
	}
	tm := NewTaskManager(backend)
	ctx := context.Background()

	taskID, err := tm.StartCommand(ctx, sandbox.CommandRequest{Command: "echo hello"})
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	if taskID == "" {
		t.Fatal("expected non-empty task ID")
	}

	snap, err := tm.Wait(ctx, taskID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if snap.State != TaskStateCompleted {
		t.Errorf("state: got %q, want %q", snap.State, TaskStateCompleted)
	}
	if snap.Output != "hello" {
		t.Errorf("output: got %q, want %q", snap.Output, "hello")
	}
}

func TestTaskManager_Cancel(t *testing.T) {
	backend := &mockBackend{
		result: sandbox.CommandResult{Stdout: []byte("done"), ExitCode: 0},
	}
	tm := NewTaskManager(backend)
	ctx := context.Background()

	taskID, _ := tm.StartCommand(ctx, sandbox.CommandRequest{Command: "sleep 10"})
	time.Sleep(10 * time.Millisecond) // let it start

	err := tm.Cancel(ctx, taskID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	snap, _ := tm.Snapshot(taskID)
	if snap.State != TaskStateCancelled {
		t.Errorf("state: got %q, want %q", snap.State, TaskStateCancelled)
	}
}

func TestTaskManager_FailedCommand(t *testing.T) {
	backend := &mockBackend{
		result: sandbox.CommandResult{Stderr: []byte("error"), ExitCode: 1},
	}
	tm := NewTaskManager(backend)
	ctx := context.Background()

	taskID, _ := tm.StartCommand(ctx, sandbox.CommandRequest{Command: "false"})
	snap, _ := tm.Wait(ctx, taskID)

	if snap.State != TaskStateFailed {
		t.Errorf("state: got %q, want %q", snap.State, TaskStateFailed)
	}
	if snap.ExitCode != 1 {
		t.Errorf("exit code: got %d, want 1", snap.ExitCode)
	}
}

func TestTaskManager_NotFound(t *testing.T) {
	tm := NewTaskManager(nil)
	_, err := tm.Wait(context.Background(), "missing")
	if err == nil {
		t.Error("expected error for missing task")
	}
}

func TestTaskManager_WriteUsesStoredAsyncSessionAcrossManagers(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryTaskStore()
	backend := newAsyncTaskBackend()
	startManager := NewTaskManagerWithStore(backend, store, "scope-1")

	taskID, err := startManager.StartCommand(ctx, sandbox.CommandRequest{Command: "cat"})
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	snap, ok, err := store.LoadTask(ctx, taskID)
	if err != nil || !ok {
		t.Fatalf("stored task = %#v, %v, %v", snap, ok, err)
	}
	if snap.SandboxSession.SessionID != "async-session-1" {
		t.Fatalf("sandbox session = %#v, want async-session-1", snap.SandboxSession)
	}

	resumeManager := NewTaskManagerWithStore(backend, store, "scope-1")
	if err := resumeManager.Write(ctx, taskID, "hello\n"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := string(backend.session.input); got != "hello\n" {
		t.Fatalf("session input = %q", got)
	}
	if backend.opened != 1 {
		t.Fatalf("OpenSessionRef calls = %d, want 1", backend.opened)
	}
}

func TestTaskManager_WaitUsesStoredAsyncSessionAfterRestart(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryTaskStore()
	backend := newAsyncTaskBackend()
	ref := sandbox.SessionRef{Backend: "async", SessionID: "async-session-1"}
	backend.session = newAsyncTaskSession(ref)
	if err := store.SaveTask(ctx, TaskSnapshot{
		TaskID:         "task-1",
		State:          TaskStateRunning,
		SessionRef:     "scope-1",
		SandboxSession: ref,
	}); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	backend.session.complete(sandbox.CommandResult{Stdout: []byte("done"), ExitCode: 0}, nil)

	resumeManager := NewTaskManagerWithStore(backend, store, "scope-1")
	snap, err := resumeManager.Wait(ctx, "task-1")
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if snap.State != TaskStateCompleted || snap.Output != "done" || snap.SandboxSession.SessionID != "async-session-1" {
		t.Fatalf("snapshot = %#v, want completed async session result", snap)
	}
	loaded, ok, err := store.LoadTask(ctx, "task-1")
	if err != nil || !ok || loaded.State != TaskStateCompleted {
		t.Fatalf("stored snapshot = %#v, %v, %v", loaded, ok, err)
	}
	if backend.opened != 1 {
		t.Fatalf("OpenSessionRef calls = %d, want 1", backend.opened)
	}
}

func TestTaskManagerUsesHostAsyncBackend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host shell test uses POSIX shell")
	}

	ctx := context.Background()
	store := NewMemoryTaskStore()
	tm := NewTaskManagerWithStore(host.New(), store, "scope-1")
	taskID, err := tm.StartCommand(ctx, sandbox.CommandRequest{Command: "printf runner-host-async"})
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	if taskID == "" {
		t.Fatal("expected task id")
	}
	snap, err := tm.Wait(ctx, taskID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if snap.State != TaskStateCompleted || strings.TrimSpace(snap.Output) != "runner-host-async" {
		t.Fatalf("snapshot = %#v, want completed host async output", snap)
	}
	loaded, ok, err := store.LoadTask(ctx, taskID)
	if err != nil || !ok || loaded.SandboxSession.SessionID == "" {
		t.Fatalf("stored snapshot = %#v ok=%v err=%v, want sandbox session ref", loaded, ok, err)
	}
}

func TestTaskAwareShellCancelsAsyncSessionWithParentContext(t *testing.T) {
	ctx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	backend := newAsyncTaskBackend()
	store := NewMemoryTaskStore()
	tm := NewTaskManagerWithStore(backend, store, "session-1")
	shell := &taskAwareShellTool{inner: &mockShellTool{}, manager: tm}

	result, err := shell.Run(&toolContext{
		Context:      ctx,
		sessionRef:   "session-1",
		invocationID: "inv-1",
		agentName:    "agent-1",
		backend:      backend,
	}, tool.Call{
		Name: "RUN_COMMAND",
		Args: map[string]any{
			"command": "long",
			"wait":    false,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	taskID, ok := result.Metadata["task_id"].(string)
	if !ok || taskID == "" {
		t.Fatalf("result metadata = %#v, want task_id", result.Metadata)
	}
	if backend.session == nil {
		t.Fatal("async session did not start")
	}

	cancelParent()
	select {
	case <-backend.session.terminated:
	case <-time.After(time.Second):
		t.Fatal("async session was not terminated after parent context cancellation")
	}

	snap, err := store.WaitTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("WaitTask() error = %v", err)
	}
	if snap.State != TaskStateCancelled {
		t.Fatalf("snapshot after parent cancel = %#v, want cancelled", snap)
	}
}

// ─── Tool augmentation tests ─────────────────────────────────────────

func TestAugmentTools_InjectsTask(t *testing.T) {
	tools := []tool.Tool{
		&mockShellTool{},
	}
	tm := NewTaskManager(nil)

	augmented := AugmentTools(tools, tm)
	if len(augmented) != 2 {
		t.Fatalf("got %d tools, want 2 (RUN_COMMAND + TASK)", len(augmented))
	}
	if augmented[1].Definition().Name != "TASK" {
		t.Errorf("second tool: got %q, want %q", augmented[1].Definition().Name, "TASK")
	}
}

func TestAugmentTools_NoDuplicateTask(t *testing.T) {
	tools := []tool.Tool{
		&mockShellTool{},
		&mockTaskTool{},
	}
	tm := NewTaskManager(nil)

	augmented := AugmentTools(tools, tm)
	if len(augmented) != 2 {
		t.Errorf("got %d, want 2 (no duplicate TASK)", len(augmented))
	}
}

func TestAugmentTools_NoTaskWithoutManager(t *testing.T) {
	tools := []tool.Tool{&mockShellTool{}}

	augmented := AugmentTools(tools, nil)
	if len(augmented) != 1 {
		t.Errorf("got %d, want 1 (no TASK without manager)", len(augmented))
	}
}

func TestAugmentTools_SpawnAlsoInjectsTask(t *testing.T) {
	tools := []tool.Tool{&mockSpawnTool{}}
	tm := NewTaskManager(nil)

	augmented := AugmentTools(tools, tm)
	if len(augmented) != 2 {
		t.Fatalf("got %d, want 2 (SPAWN + TASK)", len(augmented))
	}
}

// ─── Mock tools ──────────────────────────────────────────────────────

type mockShellTool struct{}

func (t *mockShellTool) Definition() tool.Definition {
	return tool.Definition{Name: "RUN_COMMAND"}
}
func (t *mockShellTool) Run(_ tool.Context, _ tool.Call) (tool.Result, error) {
	return tool.Result{Output: "ok"}, nil
}

type mockTaskTool struct{}

func (t *mockTaskTool) Definition() tool.Definition {
	return tool.Definition{Name: "TASK"}
}
func (t *mockTaskTool) Run(_ tool.Context, _ tool.Call) (tool.Result, error) {
	return tool.Result{Output: "ok"}, nil
}

type mockSpawnTool struct{}

func (t *mockSpawnTool) Definition() tool.Definition {
	return tool.Definition{Name: "SPAWN"}
}
func (t *mockSpawnTool) Run(_ tool.Context, _ tool.Call) (tool.Result, error) {
	return tool.Result{Output: "ok"}, nil
}

type asyncTaskBackend struct {
	session *asyncTaskSession
	opened  int
}

func newAsyncTaskBackend() *asyncTaskBackend {
	return &asyncTaskBackend{}
}

func (b *asyncTaskBackend) Name() string { return "async" }
func (b *asyncTaskBackend) Describe(_ context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{Name: "async"}, nil
}
func (b *asyncTaskBackend) Run(_ context.Context, _ sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, nil
}
func (b *asyncTaskBackend) FileSystem(_ context.Context, _ sandbox.Constraints) (sandbox.FileSystem, error) {
	return nil, nil
}
func (b *asyncTaskBackend) Status(_ context.Context) (sandbox.Status, error) {
	return sandbox.Status{Running: true}, nil
}
func (b *asyncTaskBackend) Close() error { return nil }

func (b *asyncTaskBackend) Start(_ context.Context, _ sandbox.CommandRequest) (sandbox.Session, error) {
	ref := sandbox.SessionRef{Backend: "async", SessionID: "async-session-1"}
	b.session = newAsyncTaskSession(ref)
	return b.session, nil
}

func (b *asyncTaskBackend) OpenSessionRef(ref sandbox.SessionRef) (sandbox.Session, error) {
	b.opened++
	if b.session == nil {
		b.session = newAsyncTaskSession(ref)
	}
	return b.session, nil
}

type asyncTaskSession struct {
	ref        sandbox.SessionRef
	input      []byte
	done       chan struct{}
	terminated chan struct{}
	result     sandbox.CommandResult
	err        error
}

func newAsyncTaskSession(ref sandbox.SessionRef) *asyncTaskSession {
	return &asyncTaskSession{ref: ref, done: make(chan struct{}), terminated: make(chan struct{})}
}

func (s *asyncTaskSession) Ref() sandbox.SessionRef { return s.ref }

func (s *asyncTaskSession) Terminal() sandbox.TerminalRef {
	return sandbox.TerminalRef{Backend: s.ref.Backend, SessionID: s.ref.SessionID, TerminalID: "terminal-1"}
}

func (s *asyncTaskSession) WriteInput(_ context.Context, input []byte) error {
	s.input = append(s.input, input...)
	return nil
}

func (s *asyncTaskSession) ReadOutput(_ context.Context, _, _ int64) ([]byte, []byte, int64, int64, error) {
	return s.result.Stdout, s.result.Stderr, int64(len(s.result.Stdout)), int64(len(s.result.Stderr)), nil
}

func (s *asyncTaskSession) Status(_ context.Context) (sandbox.SessionStatus, error) {
	select {
	case <-s.done:
		return sandbox.SessionStatus{SessionRef: s.ref, Running: false, SupportsInput: true, ExitCode: s.result.ExitCode}, nil
	default:
		return sandbox.SessionStatus{SessionRef: s.ref, Running: true, SupportsInput: true}, nil
	}
}

func (s *asyncTaskSession) Wait(ctx context.Context, _ time.Duration) (sandbox.SessionStatus, error) {
	select {
	case <-ctx.Done():
		return sandbox.SessionStatus{}, ctx.Err()
	case <-s.done:
		return sandbox.SessionStatus{SessionRef: s.ref, Running: false, SupportsInput: true, ExitCode: s.result.ExitCode}, nil
	}
}

func (s *asyncTaskSession) Result(ctx context.Context) (sandbox.CommandResult, error) {
	select {
	case <-ctx.Done():
		return sandbox.CommandResult{}, ctx.Err()
	case <-s.done:
		return s.result, s.err
	}
}

func (s *asyncTaskSession) Terminate(_ context.Context) error {
	select {
	case <-s.terminated:
	default:
		close(s.terminated)
	}
	s.complete(sandbox.CommandResult{ExitCode: -1}, nil)
	return nil
}

func (s *asyncTaskSession) complete(result sandbox.CommandResult, err error) {
	select {
	case <-s.done:
		return
	default:
		s.result = result
		s.err = err
		close(s.done)
	}
}
