package toolutil

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func DecodeArgs(call tool.Call) (map[string]any, error) {
	call = tool.CloneCall(call)
	if len(call.Input) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return nil, fmt.Errorf("invalid JSON input: %w", err)
	}
	return args, nil
}

// JSONResult encodes one structured business value.
func JSONResult(payload map[string]any) (tool.Result, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}, nil
}

func WithContextCancel(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
