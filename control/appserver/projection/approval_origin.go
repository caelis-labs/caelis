package projection

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// ApprovalOrigin is presentation provenance shared by live approval routing and
// durable decision replay. It grants no execution or permission authority.
type ApprovalOrigin struct {
	Scope                eventstream.Scope
	ScopeID              string
	Source               string
	Actor                string
	ParticipantID        string
	ParticipantKind      string
	ParticipantSessionID string
	ParentTool           *eventstream.ParentToolRelation
}

// ApprovalOriginFromMetadata normalizes trusted Runtime request provenance,
// including the same metadata retained in older durable pause decisions.
func ApprovalOriginFromMetadata(metadata map[string]any, ref session.SessionRef, turnID string) ApprovalOrigin {
	subagent, _ := metadata["subagent"].(bool)
	scope := eventstream.ScopeMain
	participantID := approvalOriginString(metadata, "participant_id")
	participantKind := approvalOriginString(metadata, "participant_kind")
	participantSessionID := firstNonEmpty(
		approvalOriginString(metadata, "participant_session_id"),
		approvalOriginString(metadata, "session_id"),
	)
	switch {
	case subagent:
		scope = eventstream.ScopeSubagent
	case participantID != "" || participantSessionID != "":
		scope = eventstream.ScopeParticipant
	case strings.EqualFold(approvalOriginString(metadata, "scope"), string(eventstream.ScopeSubagent)):
		scope = eventstream.ScopeSubagent
	case strings.EqualFold(approvalOriginString(metadata, "scope"), string(eventstream.ScopeParticipant)):
		scope = eventstream.ScopeParticipant
	}
	scopeID := approvalOriginString(metadata, "scope_id")
	if scopeID == "" {
		switch scope {
		case eventstream.ScopeSubagent:
			scopeID = firstNonEmpty(approvalOriginString(metadata, "task_id"), participantSessionID, participantID)
		case eventstream.ScopeParticipant:
			scopeID = firstNonEmpty(participantSessionID, participantID)
		default:
			scopeID = firstNonEmpty(ref.SessionID, turnID)
		}
	}
	var parent *eventstream.ParentToolRelation
	if callID := approvalOriginString(metadata, "parent_call_id"); callID != "" && scope == eventstream.ScopeSubagent {
		parent = &eventstream.ParentToolRelation{
			ToolCallID: callID,
			ToolName:   firstNonEmpty(approvalOriginString(metadata, "parent_tool"), approvalOriginString(metadata, "parent_tool_name")),
		}
	}
	return ApprovalOrigin{
		Scope:                scope,
		ScopeID:              scopeID,
		Source:               approvalOriginString(metadata, "source"),
		Actor:                approvalOriginString(metadata, "agent"),
		ParticipantID:        participantID,
		ParticipantKind:      participantKind,
		ParticipantSessionID: participantSessionID,
		ParentTool:           parent,
	}
}

func approvalOriginString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return strings.TrimSpace(value)
}
