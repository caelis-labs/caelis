package toolutil

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

// AnnotationMetadata returns advisory tool hints. Sandbox, approval, and
// runtime validation remain the enforcement boundary.
func AnnotationMetadata(readOnly, destructive, idempotent, openWorld bool) map[string]any {
	return map[string]any{
		"annotations": map[string]any{
			"readOnlyHint":    readOnly,
			"destructiveHint": destructive,
			"idempotentHint":  idempotent,
			"openWorldHint":   openWorld,
		},
	}
}

func DecodeArgs(call tool.Call) (map[string]any, error) {
	call = tool.CloneCall(call)
	if len(call.Input) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return nil, fmt.Errorf("tool: invalid json input: %w", err)
	}
	return args, nil
}

func JSONResult(name string, payload map[string]any, metaExtra ...map[string]any) (tool.Result, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	name = strings.TrimSpace(name)
	toolMeta := map[string]any{}
	for _, extra := range metaExtra {
		for key, value := range extra {
			if strings.TrimSpace(key) == "" {
				continue
			}
			toolMeta[key] = value
		}
	}
	var metadata map[string]any
	if len(toolMeta) > 0 {
		metadata = map[string]any{
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{
					"tool": toolMeta,
				},
			},
		}
	}
	return tool.Result{
		Name:     name,
		Content:  []model.Part{model.NewJSONPart(raw)},
		Metadata: metadata,
	}, nil
}

func WithContextCancel(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
