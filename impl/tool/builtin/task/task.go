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
		Description: "Control a yielded BASH or SPAWN task.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"wait", "write", "cancel"},
					"description": "Action to perform.",
				},
				"task_id": map[string]any{
					"type":        "string",
					"description": "Yielded task handle.",
				},
				"input": map[string]any{
					"type":        "string",
					"description": "Input for action=write.",
				},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"description": "Wait before returning.",
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
