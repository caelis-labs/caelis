package task

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const ToolName = names.Task

var allowedArgs = []string{"action", "handle", "input"}

func ValidateArgs(args map[string]any) error {
	if err := tool.RejectUnknownArgs(args, allowedArgs...); err != nil {
		return err
	}
	action, _ := args["action"].(string)
	input := ""
	if raw, present := args["input"]; present && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return tool.NewError(tool.ErrorCodeInvalidInput, "tool: arg \"input\" must be string")
		}
		input = value
	}
	if strings.EqualFold(strings.TrimSpace(action), "write") {
		if strings.TrimSpace(input) == "" {
			return tool.NewError(tool.ErrorCodeInvalidInput, "tool: arg \"input\" is required when action is write")
		}
	}
	return nil
}

// Tool is the runtime-managed async task control plane declaration.
type Tool struct{}

func New() Tool {
	return Tool{}
}

func (Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		Description: "Control asynchronous work started by RunCommand or Spawn. Use it after receiving a task handle to inspect progress or results, wait for changes, send terminal input to a running command, or cancel work. Use SendMessage for subagent communication.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"wait", "read", "write", "cancel"},
					"description": "read: RunCommand briefly waits for output; Spawn returns output_preview or exact final_message. wait may stay running for up to one minute; multiple handles return after any result. write sends stdin only to RunCommand; cancel stops work.",
				},
				"handle": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Pass one Session-scoped handle returned by RunCommand or Spawn. Only wait and cancel accept multiple comma-separated handles.",
				},
				"input": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Required only for write. Sends terminal stdin to a RunCommand task and briefly awaits its response. Spawn tasks reject write; use SendMessage instead.",
				},
			},
			"required":             []string{"action", "handle"},
			"additionalProperties": false,
			"allOf": []any{map[string]any{
				"if": map[string]any{
					"properties": map[string]any{
						"action": map[string]any{"const": "write"},
					},
					"required": []string{"action"},
				},
				"then": map[string]any{"required": []string{"input"}},
			}},
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
