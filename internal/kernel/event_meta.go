package kernel

import (
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

const (
	// EventMetaRoot is the Caelis-owned ACP extension namespace. Renderers may
	// consume values under this namespace, but should not treat provider-visible
	// tool JSON as display metadata.
	EventMetaRoot = metautil.Root

	EventMetaVersion   = metautil.Version
	EventMetaTransient = metautil.Transient
	EventMetaRuntime   = metautil.Runtime

	EventMetaRuntimeTool             = metautil.RuntimeTool
	EventMetaRuntimeToolName         = metautil.RuntimeToolName
	EventMetaRuntimeToolAction       = metautil.RuntimeToolAction
	EventMetaRuntimeToolInput        = metautil.RuntimeToolInput
	EventMetaRuntimeToolStatusDetail = metautil.RuntimeToolStatusDetail
	EventMetaRuntimeTargetKind       = metautil.RuntimeTargetKind
	EventMetaRuntimeTargetID         = metautil.RuntimeTargetID

	EventMetaRuntimeTask           = metautil.RuntimeTask
	EventMetaRuntimeTaskKind       = "kind"
	EventMetaRuntimeTaskID         = metautil.RuntimeTaskID
	EventMetaRuntimeTaskInternalID = "internal_task_id"
	EventMetaRuntimeTaskTerminalID = metautil.RuntimeTaskTerminalID
	EventMetaRuntimeTaskSessionID  = "session_id"
	EventMetaRuntimeTaskHandle     = "handle"
	EventMetaRuntimeTaskState      = "state"
	EventMetaRuntimeTaskRunning    = "running"
	EventMetaRuntimeTaskResult     = "result"
	EventMetaRuntimeTaskError      = "error"
	EventMetaRuntimeTaskFinal      = "final_message"

	EventMetaRuntimeStream             = metautil.RuntimeStream
	EventMetaRuntimeStreamMode         = metautil.RuntimeStreamMode
	EventMetaRuntimeStreamParentCallID = metautil.RuntimeStreamParentCallID
	EventMetaRuntimeStreamParentTool   = metautil.RuntimeStreamParentTool
	EventMetaRuntimeStreamParentTaskID = metautil.RuntimeStreamParentTaskID
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
