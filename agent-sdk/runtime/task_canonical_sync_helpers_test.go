package runtime

import (
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func trustedTaskResultMeta(meta map[string]any) map[string]any {
	return taskResultMeta(meta, true)
}

func taskResultMeta(meta map[string]any, trusted bool) map[string]any {
	out := session.CloneState(meta)
	if out == nil {
		out = map[string]any{}
	}
	section := taskRuntimeMetaSection(out, toolbinding.MetadataSection)
	section[toolbinding.MetadataTaskResult] = trusted
	return out
}
