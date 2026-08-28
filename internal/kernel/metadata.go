package kernel

import (
	"strings"

	"github.com/caelis-labs/caelis/internal/jsonvalue"
)

const (
	caelisMetaKey  = "caelis"
	runtimeMetaKey = "runtime"
)

func mergeKernelMeta(meta map[string]any, caelis map[string]any) map[string]any {
	if len(caelis) == 0 {
		return jsonvalue.CloneMap(meta)
	}
	return jsonvalue.MergeMap(meta, map[string]any{caelisMetaKey: caelis})
}

// approvalRelationMeta retains the legacy presentation fallback for stored
// approval envelopes. Typed ParentTool remains authoritative.
func approvalRelationMeta(parentCallID, parentTool, parentTaskID string) map[string]any {
	values := compactStrings(map[string]string{
		"parent_call_id": parentCallID,
		"parent_tool":    parentTool,
		"parent_task_id": parentTaskID,
	})
	if len(values) == 0 {
		return nil
	}
	return map[string]any{caelisMetaKey: map[string]any{
		"version": 1,
		runtimeMetaKey: map[string]any{
			"stream": values,
		},
	}}
}

func compactStrings(values map[string]string) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
