package task

import (
	"context"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/internal/toolutil"
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
		Description: "Control an async task returned by RUN_COMMAND or SPAWN. Use action=wait to collect progress or completion, action=write to send stdin to an interactive process or continue a running/waiting SPAWN child-agent conversation, and action=cancel to stop work that is no longer needed. For SPAWN child agents, prefer a single action=wait with wait_until_done=true instead of repeated short waits; it polls until the task completes or yield_time_ms elapses (default 300000 ms). When the bounded wait expires while the task is still running, the result exposes still_running and wait_timed_out. Continue an existing child agent with TASK write instead of spawning a replacement unless a separate child agent is actually needed. Always wait for a task before relying on its result.",
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
					"minLength":   1,
					"description": "Yielded task handle. For action=wait or action=cancel, multiple handles may be separated by commas.",
				},
				"input": map[string]any{
					"type":        "string",
					"description": "Input for action=write.",
				},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "Wait before returning. With wait_until_done=true, this is the bounded completion timeout; default is 300000 ms.",
				},
				"wait_until_done": map[string]any{
					"type":        "boolean",
					"description": "For action=wait, poll until the task completes or yield_time_ms elapses. If the timeout expires while the task is still running, metadata and tool output include still_running and wait_timed_out.",
				},
			},
			"required":             []string{"action", "task_id"},
			"additionalProperties": false,
		},
		Metadata: toolutil.AnnotationMetadata(false, true, false, true),
	}
}

func (Tool) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Result{}, nil
}

var _ tool.Tool = Tool{}
