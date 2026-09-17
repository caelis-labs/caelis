package kernel

import (
	"maps"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
)

func canonicalOriginFromApproval(req *agent.ApprovalRequest, fallbackRef session.SessionRef, fallbackTurnID string) *EventOrigin {
	if req == nil {
		return nil
	}
	ref := req.SessionRef
	if strings.TrimSpace(ref.SessionID) == "" {
		ref = fallbackRef
	}
	origin := acpprojector.ApprovalOriginFromMetadata(req.Metadata, ref, firstNonEmpty(req.TurnID, fallbackTurnID))
	return &EventOrigin{
		Scope: EventScope(origin.Scope), ScopeID: origin.ScopeID, Source: origin.Source, Actor: origin.Actor,
		ParticipantID: origin.ParticipantID, ParticipantKind: origin.ParticipantKind, ParticipantSessionID: origin.ParticipantSessionID,
	}
}

func metadataString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	value, ok := meta[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func canonicalApprovalPayload(req *agent.ApprovalRequest) *ApprovalPayload {
	if req == nil {
		return nil
	}
	return approval.PayloadFromRuntimeRequest(*req)
}

func cloneApprovalPayload(in *ApprovalPayload) *ApprovalPayload {
	return approval.ClonePayload(in)
}

func anyMapValue(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return maps.Clone(typed)
	}
	return nil
}
