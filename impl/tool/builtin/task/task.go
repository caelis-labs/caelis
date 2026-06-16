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
		Description: "Control an async task returned by RUN_COMMAND or SPAWN. For RUN_COMMAND, write sends terminal stdin. For SPAWN, write sends a follow-up prompt only after the child task has completed. Always wait before relying on a task result.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"wait", "write", "cancel"},
					"description": "Control action: wait observes, write delivers input, cancel stops.",
				},
				"task_id": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "One or more task handles from returned JSON task_id fields. Separate multiple handles with commas.",
				},
				"input": map[string]any{
					"type":        "string",
					"description": "Text for write. RUN_COMMAND receives terminal stdin; completed SPAWN receives a follow-up prompt.",
				},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"minimum":     -1,
					"description": "Milliseconds to wait before returning. For wait, omitted or 0 uses the default 7000 ms, -1 waits as long as possible for completion, and positive values use that exact budget.",
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
