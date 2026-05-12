package task

import (
	"context"

	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const ToolName = "TASK"

// Tool is the runtime-managed async task control plane declaration.
type Tool struct{}

func New() Tool {
	return Tool{}
}

func (Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		Description: "Control a previously yielded async task from BASH or SPAWN. Use wait to check progress, write to send stdin to BASH or a follow-up prompt to a completed SPAWN child, and cancel to stop a running task.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"wait", "write", "cancel"},
					"description": "Task control action.",
				},
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task handle returned by BASH or SPAWN after yielding.",
				},
				"input": map[string]any{
					"type":        "string",
					"description": "For action=write: stdin for BASH; follow-up prompt for a completed SPAWN child. Running SPAWN tasks must be checked with wait before write.",
				},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"description": "Optional wait window before control returns again.",
				},
			},
			"required":             []string{"action", "task_id"},
			"additionalProperties": false,
		},
	}
}

func (Tool) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Result{}, nil
}

var _ tool.Tool = Tool{}
