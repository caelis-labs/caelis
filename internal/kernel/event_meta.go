package kernel

import (
	"maps"
	"strings"
)

const (
	// EventMetaRoot is the Caelis-owned ACP extension namespace. Renderers may
	// consume values under this namespace, but should not treat provider-visible
	// tool JSON as display metadata.
	EventMetaRoot = "caelis"

	EventMetaVersion   = "version"
	EventMetaTransient = "transient"
	EventMetaRuntime   = "runtime"

	EventMetaRuntimeTool       = "tool"
	EventMetaRuntimeToolName   = "name"
	EventMetaRuntimeToolAction = "action"
	EventMetaRuntimeToolInput  = "input"
	EventMetaRuntimeTargetKind = "target_kind"
	EventMetaRuntimeTargetID   = "target_id"

	EventMetaRuntimeStream             = "stream"
	EventMetaRuntimeStreamMode         = "mode"
	EventMetaRuntimeStreamParentCallID = "parent_call_id"
	EventMetaRuntimeStreamParentTool   = "parent_tool"
	EventMetaRuntimeStreamParentTaskID = "parent_task_id"
)

// EventMetaString returns a trimmed string from _meta using a stable path.
func EventMetaString(values map[string]any, path ...string) string {
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

// EventMetaBool returns a boolean from _meta using a stable path.
func EventMetaBool(values map[string]any, path ...string) bool {
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

func withCaelisRuntimeSection(meta map[string]any, section string, values map[string]any) map[string]any {
	out := maps.Clone(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis, _ := out[EventMetaRoot].(map[string]any)
	caelis = maps.Clone(caelis)
	if caelis == nil {
		caelis = map[string]any{}
	}
	caelis[EventMetaVersion] = 1
	runtimeMeta, _ := caelis[EventMetaRuntime].(map[string]any)
	runtimeMeta = maps.Clone(runtimeMeta)
	if runtimeMeta == nil {
		runtimeMeta = map[string]any{}
	}
	sectionMeta, _ := runtimeMeta[section].(map[string]any)
	sectionMeta = maps.Clone(sectionMeta)
	if sectionMeta == nil {
		sectionMeta = map[string]any{}
	}
	for key, value := range values {
		if text, ok := value.(string); ok {
			if strings.TrimSpace(text) == "" {
				continue
			}
			sectionMeta[key] = strings.TrimSpace(text)
			continue
		}
		if value != nil {
			sectionMeta[key] = value
		}
	}
	runtimeMeta[section] = sectionMeta
	caelis[EventMetaRuntime] = runtimeMeta
	out[EventMetaRoot] = caelis
	return out
}

func mergeEventMeta(base map[string]any, overlay map[string]any) map[string]any {
	if len(base) == 0 {
		return cloneEventMetaMap(overlay)
	}
	if len(overlay) == 0 {
		return cloneEventMetaMap(base)
	}
	out := cloneEventMetaMap(base)
	for key, value := range overlay {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overlayMap, ok := value.(map[string]any); ok {
				out[key] = mergeEventMeta(baseMap, overlayMap)
				continue
			}
		}
		out[key] = cloneEventMetaValue(value)
	}
	return out
}

func cloneEventMetaMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = cloneEventMetaValue(value)
	}
	return out
}

func cloneEventMetaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneEventMetaMap(typed)
	default:
		return value
	}
}
