package task

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const ToolName = names.Task

var allowedArgs = []string{"action", "task_id", "input", "yield_time_ms"}

func ValidateArgs(args map[string]any) error {
	return tool.RejectUnknownArgs(args, allowedArgs...)
}

// Tool is the runtime-managed async task control plane declaration.
type Tool struct{}

func New() Tool {
	return Tool{}
}

func (Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		Description: "Control an async task returned by RunCommand or Spawn. For RunCommand, write sends terminal stdin. For Spawn, write sends a follow-up prompt only after the child task has completed. Always wait before relying on a task result.",
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
					"description": "Text for write. RunCommand receives terminal stdin; completed Spawn receives a follow-up prompt.",
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

func (Tool) Call(_ context.Context, call tool.Call) (tool.Result, error) {
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	if err := ValidateArgs(args); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{}, fmt.Errorf("tool: Task must be executed by the runtime wrapper")
}

var _ tool.Tool = Tool{}
