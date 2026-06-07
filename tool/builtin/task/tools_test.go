package task

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/tool"
)

type fakeToolContext struct {
	context.Context
}

func (c fakeToolContext) SessionRef() string             { return "session-1" }
func (c fakeToolContext) InvocationID() string           { return "inv-1" }
func (c fakeToolContext) AgentName() string              { return "agent-1" }
func (c fakeToolContext) FileSystem() sandbox.FileSystem { return nil }

type fakeController struct {
	waited    string
	wroteID   string
	wroteText string
	cancelled string
}

func (c *fakeController) Wait(_ tool.Context, taskID string) (TaskSnapshot, error) {
	c.waited = taskID
	return TaskSnapshot{TaskID: taskID, State: "completed", Output: "done", ExitCode: 0}, nil
}

func (c *fakeController) Write(_ tool.Context, taskID string, input string) error {
	c.wroteID = taskID
	c.wroteText = input
	return nil
}

func (c *fakeController) Cancel(_ tool.Context, taskID string) error {
	c.cancelled = taskID
	return nil
}

func TestTaskToolRequiresActionAndTaskID(t *testing.T) {
	result, err := New(&fakeController{}).Run(testCtx(), tool.Call{Args: map[string]any{}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.IsError {
		t.Fatalf("result = %#v, want error", result)
	}
}

func TestTaskToolRequiresController(t *testing.T) {
	result, err := All()[0].Run(testCtx(), tool.Call{Args: map[string]any{
		"action":  "wait",
		"task_id": "task-1",
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.IsError || result.Output != "TASK: no controller configured" {
		t.Fatalf("result = %#v, want no-controller error", result)
	}
}

func TestTaskToolWaitWriteCancel(t *testing.T) {
	controller := &fakeController{}
	toolUnderTest := New(controller)
	ctx := testCtx()

	wait, err := toolUnderTest.Run(ctx, tool.Call{Args: map[string]any{
		"action":  "wait",
		"task_id": "task-1",
	}})
	if err != nil {
		t.Fatalf("wait Run() error = %v", err)
	}
	if wait.Output != "done" || wait.Metadata["task_id"] != "task-1" {
		t.Fatalf("wait result = %#v, want completed snapshot", wait)
	}

	write, err := toolUnderTest.Run(ctx, tool.Call{Args: map[string]any{
		"action":  "write",
		"task_id": "task-1",
		"input":   "hello",
	}})
	if err != nil {
		t.Fatalf("write Run() error = %v", err)
	}
	if write.Output != "ok" || controller.wroteID != "task-1" || controller.wroteText != "hello" {
		t.Fatalf("write result = %#v controller = %#v", write, controller)
	}

	cancel, err := toolUnderTest.Run(ctx, tool.Call{Args: map[string]any{
		"action":  "cancel",
		"task_id": "task-1",
	}})
	if err != nil {
		t.Fatalf("cancel Run() error = %v", err)
	}
	if cancel.Output != "cancelled" || controller.cancelled != "task-1" {
		t.Fatalf("cancel result = %#v controller = %#v", cancel, controller)
	}
}

func testCtx() tool.Context {
	return fakeToolContext{Context: context.Background()}
}
