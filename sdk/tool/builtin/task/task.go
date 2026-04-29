package task

import (
	"context"

	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

const ToolName = "TASK"

// Tool is the runtime-managed async task control plane declaration.
type Tool struct{}

func New() Tool {
	return Tool{}
}

func (Tool) Definition() sdktool.Definition {
	return sdktool.Definition{
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

func (Tool) Call(context.Context, sdktool.Call) (sdktool.Result, error) {
	return sdktool.Result{}, nil
}

var _ sdktool.Tool = Tool{}
