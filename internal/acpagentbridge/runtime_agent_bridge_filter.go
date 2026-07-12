package acpagentbridge

import (
	"github.com/caelis-labs/caelis/agent-sdk/session"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func suppressACPBridgeSubagentEvent(event *session.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	participant := event.Scope.Participant
	if participant.Kind == session.ParticipantKindSubagent &&
		participant.Role == session.ParticipantRoleDelegated {
		return true
	}
	return suppressACPBridgeSubagentMeta(event.Meta)
}

func suppressACPBridgeSubagentEnvelope(env eventstream.Envelope) bool {
	if env.Scope != eventstream.ScopeSubagent {
		return false
	}
	return suppressACPBridgeSubagentMeta(env.Meta)
}

func suppressACPBridgeSubagentMeta(meta map[string]any) bool {
	parentTool := metautil.String(
		meta,
		metautil.Root,
		metautil.Runtime,
		metautil.RuntimeStream,
		metautil.RuntimeStreamParentTool,
	)
	canonical, _ := names.Resolve(parentTool)
	switch canonical {
	case names.Spawn, names.Task:
		return true
	default:
		return metautil.Bool(
			meta,
			metautil.Root,
			metautil.Runtime,
			metautil.RuntimeStream,
			metautil.RuntimeStreamMirroredToParentTool,
		)
	}
}
