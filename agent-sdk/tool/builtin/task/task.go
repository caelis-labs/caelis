package task

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const ToolName = names.Task

var allowedArgs = []string{"action", "handle", "input", "yield_time_ms"}

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
					"description": "Control action: wait observes for at most one minute, write delivers input, cancel stops.",
				},
				"handle": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "One or more Session-scoped handles returned by RunCommand or Spawn. Separate multiple handles with commas.",
				},
				"input": map[string]any{
					"type":        "string",
					"description": "Text for write. RunCommand receives terminal stdin; completed Spawn receives a follow-up prompt.",
				},
			},
			"required":             []string{"action", "handle"},
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
