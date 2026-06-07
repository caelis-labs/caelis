package task

import (
	"fmt"

	"github.com/OnslaughtSnail/caelis/tool"
)

// taskTool implements the TASK tool for async task control.
type taskTool struct {
	controller Controller
}

// Controller is the contract for managing async tasks.
type Controller interface {
	// Wait blocks until a task completes or context cancels.
	Wait(ctx tool.Context, taskID string) (TaskSnapshot, error)
	// Write sends input to a running task.
	Write(ctx tool.Context, taskID string, input string) error
	// Cancel stops a running task.
	Cancel(ctx tool.Context, taskID string) error
}

// TaskSnapshot is the current state of a task.
type TaskSnapshot struct {
	TaskID   string
	State    string // "running", "completed", "error", "cancelled"
	Output   string
	ExitCode int
	Error    string
}

func New(controller Controller) tool.Tool {
	return &taskTool{controller: controller}
}

func (*taskTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "TASK",
		Description: "Control async tasks (wait, write, cancel).",
		Schema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"action":  {Type: "string", Description: "Action: wait, write, cancel", Enum: []any{"wait", "write", "cancel"}},
				"task_id": {Type: "string", Description: "Task handle"},
				"input":   {Type: "string", Description: "Input for write action"},
			},
			Required: []string{"action", "task_id"},
		},
		Metadata: map[string]any{
			"requires_task_controller": true,
		},
	}
}

func (t *taskTool) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	action, _ := call.Args["action"].(string)
	taskID, _ := call.Args["task_id"].(string)
	if action == "" || taskID == "" {
		return tool.Result{Output: "action and task_id are required", IsError: true}, nil
	}

	if t.controller == nil {
		return tool.Result{Output: "TASK: no controller configured", IsError: true}, nil
	}

	switch action {
	case "wait":
		snapshot, err := t.controller.Wait(ctx, taskID)
		if err != nil {
			return tool.Result{Output: fmt.Sprintf("wait error: %v", err), IsError: true}, nil
		}
		return tool.Result{
			Output: snapshot.Output,
			Metadata: map[string]any{
				"task_id":   snapshot.TaskID,
				"state":     snapshot.State,
				"exit_code": snapshot.ExitCode,
			},
			IsError: snapshot.State == "error",
		}, nil

	case "write":
		input, _ := call.Args["input"].(string)
		if err := t.controller.Write(ctx, taskID, input); err != nil {
			return tool.Result{Output: fmt.Sprintf("write error: %v", err), IsError: true}, nil
		}
		return tool.Result{Output: "ok"}, nil

	case "cancel":
		if err := t.controller.Cancel(ctx, taskID); err != nil {
			return tool.Result{Output: fmt.Sprintf("cancel error: %v", err), IsError: true}, nil
		}
		return tool.Result{Output: "cancelled"}, nil

	default:
		return tool.Result{Output: fmt.Sprintf("unknown action: %s", action), IsError: true}, nil
	}
}

// All returns all task built-in tools (requires controller to be set).
func All() []tool.Tool {
	return []tool.Tool{&taskTool{}}
}
