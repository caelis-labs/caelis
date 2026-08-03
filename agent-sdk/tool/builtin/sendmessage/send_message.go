// Package sendmessage declares the Agent-to-Agent message tool. Runtime owns
// routing and identity; the model supplies only a target and message body.
package sendmessage

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const ToolName = names.SendMessage

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

// Tool is the runtime-managed Agent message declaration.
type Tool struct{}

func New() Tool { return Tool{} }

func (Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		Description: "Send an incremental update or question to your parent Agent or another available subagent. Keep your terminal answer in the final response so the parent retrieves it with Task read or wait; do not send the same terminal answer through both channels. Success means the routing boundary owns delivery, not that the target finished. In the result, state is delivery or target observation, started_turn means a child activity started, and turn_id only groups that activity. If state is unknown_outcome, do not resend it as a new call.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to": map[string]any{
					"type": "string", "minLength": 1,
					"description": "Use parent for the parent Agent, or a Session-scoped subagent handle returned by Spawn.",
				},
				"message": map[string]any{
					"type": "string", "minLength": 1,
					"description": "The message to deliver.",
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
