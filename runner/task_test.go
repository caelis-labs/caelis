package runner

import (
	"context"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/sandbox"
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
