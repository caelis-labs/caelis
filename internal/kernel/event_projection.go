package kernel

import (
	"maps"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func replayClientEvents(events []*session.Event) []*session.Event {
	return session.FilterClientReplayEvents(events)
}

func replayControlPlaneEvents(events []*session.Event, includeTransient bool) []*session.Event {
	if includeTransient {
		return events
	}
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if session.IsCanonicalHistoryEvent(event) {
			out = append(out, event)
		}
	}
	return out
}

func cursorNotFoundError(cursor string) error {
	return &Error{
		Kind:        KindNotFound,
		Code:        CodeCursorNotFound,
		UserVisible: true,
		Message:     "gateway: cursor not found",
		Detail:      cursor,
	}
}

func canonicalOriginFromApproval(req *agent.ApprovalRequest, fallbackRef session.SessionRef, fallbackTurnID string) *EventOrigin {
	if req == nil {
		return nil
	}
	ref := req.SessionRef
	if strings.TrimSpace(ref.SessionID) == "" {
		ref = fallbackRef
	}
	scope := EventScopeMain
	participantID := metadataString(req.Metadata, "participant_id")
	participantKind := metadataString(req.Metadata, "participant_kind")
	participantSessionID := firstNonEmpty(
		metadataString(req.Metadata, "participant_session_id"),
		metadataString(req.Metadata, "session_id"),
	)
	turnID := firstNonEmpty(strings.TrimSpace(req.TurnID), strings.TrimSpace(fallbackTurnID))
	switch {
	case metadataBool(req.Metadata, "subagent"):
		scope = EventScopeSubagent
	case participantID != "" || participantSessionID != "":
		scope = EventScopeParticipant
	case strings.EqualFold(metadataString(req.Metadata, "scope"), string(EventScopeSubagent)):
		scope = EventScopeSubagent
	case strings.EqualFold(metadataString(req.Metadata, "scope"), string(EventScopeParticipant)):
		scope = EventScopeParticipant
	}
	scopeID := firstNonEmpty(
		metadataString(req.Metadata, "scope_id"),
	)
	if scopeID == "" {
		switch scope {
		case EventScopeSubagent:
			scopeID = firstNonEmpty(metadataString(req.Metadata, "task_id"), participantSessionID, participantID)
		case EventScopeParticipant:
			scopeID = firstNonEmpty(participantSessionID, participantID)
		default:
			scopeID = canonicalScopeID(ref, scope, participantID, participantSessionID, turnID)
		}
	}
	if scope == EventScopeMain && scopeID == "" {
		scopeID = canonicalScopeID(ref, scope, participantID, participantSessionID, turnID)
	}
	return &EventOrigin{
		Scope:                scope,
		ScopeID:              scopeID,
		Source:               metadataString(req.Metadata, "source"),
		Actor:                metadataString(req.Metadata, "agent"),
		ParticipantID:        participantID,
		ParticipantKind:      participantKind,
		ParticipantSessionID: participantSessionID,
	}
}

func canonicalScopeID(ref session.SessionRef, scope EventScope, participantID string, participantSessionID string, turnID string) string {
	switch scope {
	case EventScopeParticipant:
		return firstNonEmpty(strings.TrimSpace(turnID), participantSessionID, participantID)
	case EventScopeSubagent:
		return firstNonEmpty(participantSessionID, participantID, strings.TrimSpace(turnID))
	default:
		return firstNonEmpty(strings.TrimSpace(ref.SessionID), strings.TrimSpace(turnID))
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

func metadataBool(meta map[string]any, key string) bool {
	if len(meta) == 0 {
		return false
	}
	value, ok := meta[key]
	if !ok {
		return false
	}
	flag, ok := value.(bool)
	return ok && flag
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
