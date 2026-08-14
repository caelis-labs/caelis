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

var allowedArgs = []string{"action", "handle", "input", "append_newline"}

func ValidateArgs(args map[string]any) error {
	if err := tool.RejectUnknownArgs(args, allowedArgs...); err != nil {
		return err
	}
	action, _ := args["action"].(string)
	appendExact := false
	if raw, present := args["append_newline"]; present && raw != nil {
		value, ok := raw.(bool)
		if !ok {
			return tool.NewError(tool.ErrorCodeInvalidInput, "tool: arg \"append_newline\" must be boolean")
		}
		appendExact = !value
	}
	input := ""
	rawInput, inputPresent := args["input"]
	if inputPresent && rawInput != nil {
		value, ok := rawInput.(string)
		if !ok {
			return tool.NewError(tool.ErrorCodeInvalidInput, "tool: arg \"input\" must be string")
		}
		input = value
	}
	if strings.EqualFold(strings.TrimSpace(action), "write") {
		if input == "" || (strings.TrimSpace(input) == "" && !appendExact) {
			return tool.NewError(tool.ErrorCodeInvalidInput, "tool: arg \"input\" is required when action is write")
		}
	} else if inputPresent || args["append_newline"] != nil {
		return tool.NewError(tool.ErrorCodeInvalidInput, "tool: args \"input\" and \"append_newline\" are valid only when action is write")
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
		Description: "Inspect or control asynchronous work using a Task handle.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"wait", "read", "write", "cancel"},
					"description": "read inspects now; wait observes for up to one minute; write sends stdin only to an input-capable command; cancel interrupts the current activity.",
				},
				"handle": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "A Session-scoped Task handle; wait/cancel may use a comma-separated list.",
				},
				"input": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Command stdin; valid only for write on an input-capable command.",
				},
				"append_newline": map[string]any{
					"type":        "boolean",
					"default":     true,
					"description": "Append a newline to input unless it already ends in one; set false to send exact terminal bytes such as escape sequences or control characters. Valid only for write.",
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
				"else": map[string]any{"not": map[string]any{"anyOf": []any{
					map[string]any{"required": []string{"input"}},
					map[string]any{"required": []string{"append_newline"}},
				}}},
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
