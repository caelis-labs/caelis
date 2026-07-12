package acpagentbridge

import (
	"github.com/caelis-labs/caelis/agent-sdk/session"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
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
	return suppressACPBridgeSubagentRelationDelivery(eventstream.ResolveRelationDelivery(eventstream.Envelope{Meta: event.Meta}))
}

func suppressACPBridgeSubagentEnvelope(env eventstream.Envelope) bool {
	if env.Scope != eventstream.ScopeSubagent {
		return false
	}
	return suppressACPBridgeSubagentRelationDelivery(eventstream.ResolveRelationDelivery(env))
}

func suppressACPBridgeSubagentRelationDelivery(relationDelivery eventstream.RelationDelivery) bool {
	if parentToolIsACPBridgeSubagentCompatibilityRelation(relationDelivery.ParentTool) {
		return true
	}
	return relationDelivery.Delivery != nil && relationDelivery.Delivery.HasParentToolMirror
}

func parentToolIsACPBridgeSubagentCompatibilityRelation(parentTool *eventstream.ParentToolRelation) bool {
	if parentTool == nil {
		return false
	}
	canonical, _ := names.Resolve(parentTool.ToolName)
	return canonical == names.Spawn || canonical == names.Task
}
