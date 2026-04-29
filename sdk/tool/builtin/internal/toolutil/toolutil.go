package toolutil

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func DecodeArgs(call sdktool.Call) (map[string]any, error) {
	call = sdktool.CloneCall(call)
	if len(call.Input) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return nil, fmt.Errorf("tool: invalid json input: %w", err)
	}
	return args, nil
}

func JSONResult(name string, payload map[string]any) (sdktool.Result, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return sdktool.Result{}, err
	}
	name = strings.TrimSpace(name)
	return sdktool.Result{
		Name:    name,
		Content: []sdkmodel.Part{sdkmodel.NewJSONPart(raw)},
		Meta:    maps.Clone(payload),
	}, nil
}

func JSONErrorResult(name string, payload map[string]any) (sdktool.Result, error) {
	out, err := JSONResult(name, payload)
	out.IsError = true
	return out, err
}

func WithContextCancel(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
