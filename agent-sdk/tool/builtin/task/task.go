package task

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/names"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

const ToolName = names.Task

// MaxWaitMilliseconds bounds one model-requested Task observation.
const MaxWaitMilliseconds = 300000

var allowedArgs = []string{"action", "handle", "input", "append_newline", "yield_time_ms"}

func ValidateArgs(args map[string]any) error {
	if err := tool.RejectUnknownArgs(args, allowedArgs...); err != nil {
		return err
	}
	action, _ := args["action"].(string)
	if raw, present := args["yield_time_ms"]; present {
		if !strings.EqualFold(strings.TrimSpace(action), "wait") {
			return tool.NewError(tool.ErrorCodeInvalidInput, "yield_time_ms is valid only for wait")
		}
		n, ok := raw.(float64)
		if !ok || n != math.Trunc(n) || n < 0 || n > MaxWaitMilliseconds {
			return tool.NewError(tool.ErrorCodeInvalidInput, "yield_time_ms must be an integer from 0 to 300000")
		}
	}
	appendExact := false
	if raw, present := args["append_newline"]; present && raw != nil {
		value, ok := raw.(bool)
		if !ok {
			return tool.NewError(tool.ErrorCodeInvalidInput, "arg \"append_newline\" must be boolean")
		}
		appendExact = !value
	}
	input := ""
	rawInput, inputPresent := args["input"]
	if inputPresent && rawInput != nil {
		value, ok := rawInput.(string)
		if !ok {
			return tool.NewError(tool.ErrorCodeInvalidInput, "arg \"input\" must be string")
		}
		input = value
	}
	if strings.EqualFold(strings.TrimSpace(action), "write") {
		if input == "" || (strings.TrimSpace(input) == "" && !appendExact) {
			return tool.NewError(tool.ErrorCodeInvalidInput, "arg \"input\" is required when action is write")
		}
	} else if inputPresent || args["append_newline"] != nil {
		return tool.NewError(tool.ErrorCodeInvalidInput, "args \"input\" and \"append_newline\" are valid only when action is write")
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
		Name:         ToolName,
		Description:  "Inspect or control asynchronous work using a Task handle. Choose wait duration by expected remaining time. For CI or long builds use 60–300 seconds and increase the interval if still running. Use a bounded readiness probe for servers.",
		OutputSchema: map[string]any{"type": "object"},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"wait", "write", "cancel"},
					"description": "wait observes the original Task and returns state and available output on completion or yield expiry. write sends supported input. cancel requests cancellation when supported.",
				},
				"handle": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "A Session-scoped job handle. wait and cancel accept a comma-separated list.",
				},
				"yield_time_ms": map[string]any{
					"type": "integer", "minimum": 0, "maximum": MaxWaitMilliseconds, "default": 60000,
					"description": "Wait only. Omit for 60 seconds; use 0 for an immediate snapshot when you have other work.",
				},
				"input": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Job input; valid only for write on an input-capable Job.",
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
				"if":   map[string]any{"required": []string{"yield_time_ms"}},
				"then": map[string]any{"properties": map[string]any{"action": map[string]any{"const": "wait"}}},
			}, map[string]any{
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
	return tool.Result{}, fmt.Errorf("task must be executed by the runtime wrapper")
}

var _ tool.Tool = Tool{}
