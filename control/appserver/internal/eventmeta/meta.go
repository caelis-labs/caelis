// Package eventmeta owns metadata shaping shared only by Control AppServer feed
// projection, Task observation, wire encoding, and replay.
package eventmeta

import (
	"encoding/json"
	"maps"
	"math"
	"strings"
)

const metaVersionKey = "version"

const (
	Root    = "caelis"
	Runtime = "runtime"

	RuntimeTool     = "tool"
	RuntimeToolName = "name"

	RuntimeTask           = "task"
	RuntimeTaskTerminalID = "terminal_id"
	RuntimeOutputCursor   = "output_cursor"
	RuntimeOutputStart    = "output_start_cursor"
	RuntimeOutputDelta    = "output_delta"

	RuntimeStream             = "stream"
	RuntimeStreamMode         = "mode"
	RuntimeStreamParentCallID = "parent_call_id"
	RuntimeStreamParentTool   = "parent_tool"
)

func CloneMap(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := maps.Clone(meta)
	for key, value := range out {
		out[key] = cloneValue(value)
	}
	return out
}

func Merge(base map[string]any, extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return CloneMap(base)
	}
	out := CloneMap(base)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range extra {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overlayMap, ok := value.(map[string]any); ok {
				out[key] = Merge(baseMap, overlayMap)
				continue
			}
		}
		out[key] = cloneValue(value)
	}
	return out
}

func String(values map[string]any, path ...string) string {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapped[key]
	}
	text, _ := current.(string)
	return strings.TrimSpace(text)
}

func Int64(values map[string]any, path ...string) (int64, bool) {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		var present bool
		current, present = mapped[key]
		if !present {
			return 0, false
		}
	}
	switch value := current.(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		const int64Limit = 1 << 63
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
			value < -int64Limit || value >= int64Limit {
			return 0, false
		}
		return int64(value), true
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func WithRuntimeSection(meta map[string]any, section string, values map[string]any) map[string]any {
	if section == "" || len(values) == 0 {
		return CloneMap(meta)
	}
	out := CloneMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis := CloneMap(mapAt(out, Root))
	if caelis == nil {
		caelis = map[string]any{}
	}
	caelis[metaVersionKey] = 1
	runtime := CloneMap(mapAt(caelis, Runtime))
	if runtime == nil {
		runtime = map[string]any{}
	}
	sectionMap := CloneMap(mapAt(runtime, section))
	if sectionMap == nil {
		sectionMap = map[string]any{}
	}
	for key, value := range values {
		sectionMap[key] = cloneValue(value)
	}
	runtime[section] = sectionMap
	caelis[Runtime] = runtime
	out[Root] = caelis
	return out
}

func WithCompactRuntimeSection(meta map[string]any, section string, values map[string]any) map[string]any {
	return WithRuntimeSection(meta, section, compactRuntimeValues(values))
}

func RuntimeSection(meta map[string]any, section string) map[string]any {
	caelis := mapAt(meta, Root)
	runtime := mapAt(caelis, Runtime)
	return CloneMap(mapAt(runtime, section))
}

// WithoutRuntimeSectionKeys returns an isolated metadata map without selected
// keys from one _meta.caelis.runtime section. Empty runtime and Caelis maps are
// removed while unrelated provider metadata and sibling sections are retained.
func WithoutRuntimeSectionKeys(meta map[string]any, section string, keys ...string) map[string]any {
	out := CloneMap(meta)
	if len(out) == 0 || section == "" || len(keys) == 0 {
		return out
	}
	caelis := CloneMap(mapAt(out, Root))
	runtime := CloneMap(mapAt(caelis, Runtime))
	sectionMap := CloneMap(mapAt(runtime, section))
	if len(sectionMap) == 0 {
		return out
	}
	for _, key := range keys {
		delete(sectionMap, key)
	}
	if len(sectionMap) == 0 {
		delete(runtime, section)
	} else {
		runtime[section] = sectionMap
	}
	if len(runtime) == 0 {
		delete(caelis, Runtime)
	} else {
		caelis[Runtime] = runtime
	}
	if len(caelis) == 0 {
		delete(out, Root)
	} else {
		out[Root] = caelis
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func compactRuntimeValues(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := map[string]any{}
	for key, value := range values {
		if text, ok := value.(string); ok {
			if strings.TrimSpace(text) == "" {
				continue
			}
			out[key] = strings.TrimSpace(text)
			continue
		}
		if value != nil {
			out[key] = cloneValue(value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mapAt(values map[string]any, key string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out, _ := values[key].(map[string]any)
	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return CloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return value
	}
}
