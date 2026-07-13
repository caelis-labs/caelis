package gateway

import "github.com/caelis-labs/caelis/protocol/acp/metautil"

const (
	// EventMetaRoot is the Caelis-owned ACP extension namespace.
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
	EventMetaRuntimeTaskID         = metautil.RuntimeTaskID
	EventMetaRuntimeTaskTerminalID = metautil.RuntimeTaskTerminalID

	EventMetaRuntimeStream             = metautil.RuntimeStream
	EventMetaRuntimeStreamMode         = metautil.RuntimeStreamMode
	EventMetaRuntimeStreamParentCallID = metautil.RuntimeStreamParentCallID
	EventMetaRuntimeStreamParentTool   = metautil.RuntimeStreamParentTool
	EventMetaRuntimeStreamParentTaskID = metautil.RuntimeStreamParentTaskID
)

// EventMetaString returns a trimmed string from _meta using a stable path.
func EventMetaString(values map[string]any, path ...string) string {
	return metautil.String(values, path...)
}

// EventMetaBool returns a boolean from _meta using a stable path.
func EventMetaBool(values map[string]any, path ...string) bool {
	return metautil.Bool(values, path...)
}
