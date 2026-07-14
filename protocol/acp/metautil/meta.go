package metautil

import (
	"maps"
	"strings"
)

const (
	// Root is the Caelis-owned ACP extension namespace. Renderers may consume
	// values under this namespace, but provider-visible tool JSON must not be
	// treated as display metadata.
	Root = "caelis"

	Version   = "version"
	Transient = "transient"
	Runtime   = "runtime"

	RuntimeTool             = "tool"
	RuntimeToolName         = "name"
	RuntimeToolAction       = "action"
	RuntimeToolInput        = "input"
	RuntimeToolStatusDetail = "status_detail"
	RuntimeTargetKind       = "target_kind"
	RuntimeTargetID         = "target_id"

	RuntimeTask           = "task"
	RuntimeTaskID         = "task_id"
	RuntimeTaskTerminalID = "terminal_id"

	RuntimeStream             = "stream"
	RuntimeStreamMode         = "mode"
	RuntimeStreamTruncated    = "truncated"
	RuntimeStreamBefore       = "truncated_before"
	RuntimeStreamParentCallID = "parent_call_id"
	RuntimeStreamParentTool   = "parent_tool"
	RuntimeStreamParentTaskID = "parent_task_id"
)

// String returns a trimmed string from _meta using a stable path.
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

// Bool returns a boolean from _meta using a stable path.
func Bool(values map[string]any, path ...string) bool {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current = mapped[key]
	}
	value, _ := current.(bool)
	return value
}

// WithRuntimeSection returns a copy of meta with one _meta.caelis.runtime
// section merged in. It preserves sibling runtime sections.
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
	caelis[Version] = 1
	runtime := CloneMap(mapAt(caelis, Runtime))
	if runtime == nil {
		runtime = map[string]any{}
	}
	sectionMap := CloneMap(mapAt(runtime, section))
	if sectionMap == nil {
		sectionMap = map[string]any{}
	}
	for key, value := range values {
		sectionMap[key] = CloneAny(value)
	}
	runtime[section] = sectionMap
	caelis[Runtime] = runtime
	out[Root] = caelis
	return out
}

// WithCompactRuntimeSection is the canonical helper for durable kernel runtime
// metadata. It trims string values and drops empty strings and nils before
// merging the section.
func WithCompactRuntimeSection(meta map[string]any, section string, values map[string]any) map[string]any {
	return WithRuntimeSection(meta, section, compactRuntimeValues(values))
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
			out[key] = CloneAny(value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func RuntimeSection(meta map[string]any, section string) map[string]any {
	caelis := mapAt(meta, Root)
	runtime := mapAt(caelis, Runtime)
	return CloneMap(mapAt(runtime, section))
}

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
		out[key] = CloneAny(value)
	}
	return out
}

func CloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return CloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = CloneAny(item)
		}
		return out
	default:
		return value
	}
}

func CloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := maps.Clone(in)
	for key, value := range out {
		out[key] = CloneAny(value)
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
