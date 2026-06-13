package gateway

import "strings"

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

	EventMetaRuntimeStream                     = "stream"
	EventMetaRuntimeStreamMode                 = "mode"
	EventMetaRuntimeStreamParentCallID         = "parent_call_id"
	EventMetaRuntimeStreamParentTool           = "parent_tool"
	EventMetaRuntimeStreamParentTaskID         = "parent_task_id"
	EventMetaRuntimeStreamMirroredToParentTool = "mirrored_to_parent_tool"
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
