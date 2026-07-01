package kernel

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
)

const (
	// EventMetaRoot is the Caelis-owned ACP extension namespace. Renderers may
	// consume values under this namespace, but should not treat provider-visible
	// tool JSON as display metadata.
	EventMetaRoot = metautil.Root

	EventMetaVersion   = metautil.Version
	EventMetaTransient = "transient"
	EventMetaRuntime   = metautil.Runtime

	EventMetaRuntimeTool       = "tool"
	EventMetaRuntimeToolName   = "name"
	EventMetaRuntimeToolAction = "action"
	EventMetaRuntimeToolInput  = "input"
	EventMetaRuntimeTargetKind = "target_kind"
	EventMetaRuntimeTargetID   = "target_id"

	EventMetaRuntimeTask           = "task"
	EventMetaRuntimeTaskKind       = "kind"
	EventMetaRuntimeTaskID         = "task_id"
	EventMetaRuntimeTaskInternalID = "internal_task_id"
	EventMetaRuntimeTaskTerminalID = "terminal_id"
	EventMetaRuntimeTaskSessionID  = "session_id"
	EventMetaRuntimeTaskHandle     = "handle"
	EventMetaRuntimeTaskState      = "state"
	EventMetaRuntimeTaskRunning    = "running"
	EventMetaRuntimeTaskResult     = "result"
	EventMetaRuntimeTaskError      = "error"
	EventMetaRuntimeTaskFinal      = "final_message"

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

func withCaelisRuntimeSection(meta map[string]any, section string, values map[string]any) map[string]any {
	return metautil.WithCompactRuntimeSection(meta, section, values)
}

func mergeEventMeta(base map[string]any, overlay map[string]any) map[string]any {
	return metautil.Merge(base, overlay)
}

func cloneEventMetaMap(values map[string]any) map[string]any {
	return metautil.CloneMap(values)
}
