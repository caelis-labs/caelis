// Package sendmessage declares the Agent-to-Agent input tool. Runtime owns
// routing and identity; the model supplies only a target and input body.
package sendmessage

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

const ToolName = "SendMessage"

var allowedArgs = []string{"to", "message"}

func ValidateArgs(args map[string]any) error {
	if err := tool.RejectUnknownArgs(args, allowedArgs...); err != nil {
		return err
	}
	for _, name := range allowedArgs {
		value, ok := args[name].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("tool: arg %q is required", name))
		}
	}
	return nil
}

// Tool is the runtime-managed Agent input declaration.
type Tool struct{}

func New() Tool { return Tool{} }

func (Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		Description: "Send ordinary input to another Agent or the parent Agent.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to": map[string]any{
					"type": "string", "minLength": 1,
					"description": "Target Agent handle, or parent.",
				},
				"message": map[string]any{
					"type": "string", "minLength": 1,
					"description": "Input to send.",
				},
			},
			"required":             []string{"to", "message"},
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
	return tool.Result{}, fmt.Errorf("tool: SendMessage must be executed by the runtime wrapper")
}

var _ tool.Tool = Tool{}
