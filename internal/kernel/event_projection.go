package kernel

import (
	"maps"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
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
