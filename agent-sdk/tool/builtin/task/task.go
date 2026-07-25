package task

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const ToolName = names.Task

var allowedArgs = []string{"action", "handle", "input"}

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
		Description: "Control an async task returned by RunCommand or Spawn. Read accepts exactly one handle: for RunCommand it briefly waits for new output without requiring exit; for Spawn it immediately returns the current state with output_preview while running or the exact final_message after completion. RunCommand write sends terminal stdin then briefly awaits its response. Completed Spawn write sends a follow-up prompt. Wait observes either target for at most one minute and may return state=running; call it again when needed.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"wait", "read", "write", "cancel"},
					"description": "wait: one or more RunCommand or Spawn handles; read: exactly one handle, output-driven for RunCommand and an immediate snapshot for Spawn; write: exactly one handle; cancel: one or more handles.",
				},
				"handle": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "A Session-scoped handle returned by RunCommand or Spawn. Only wait and cancel accept comma-separated handles.",
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
