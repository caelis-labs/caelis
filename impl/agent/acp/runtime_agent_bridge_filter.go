package acp

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
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
	parentTool := gateway.EventMetaString(
		meta,
		gateway.EventMetaRoot,
		gateway.EventMetaRuntime,
		gateway.EventMetaRuntimeStream,
		gateway.EventMetaRuntimeStreamParentTool,
	)
	switch strings.ToUpper(strings.TrimSpace(parentTool)) {
	case "SPAWN", "TASK":
		return true
	default:
		return gateway.EventMetaBool(
			meta,
			gateway.EventMetaRoot,
			gateway.EventMetaRuntime,
			gateway.EventMetaRuntimeStream,
			gateway.EventMetaRuntimeStreamMirroredToParentTool,
		)
	}
}
