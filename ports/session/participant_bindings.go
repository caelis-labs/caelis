package session

import "strings"

// PutParticipantBinding creates or replaces one participant binding in a
// session. Empty participant IDs are appended to preserve existing store
// behavior for invalid or synthetic bindings.
func PutParticipantBinding(activeSession *Session, binding ParticipantBinding) bool {
	if activeSession == nil {
		return false
	}
	normalized := CloneParticipantBinding(binding)
	for i := range activeSession.Participants {
		if activeSession.Participants[i].ID == normalized.ID && normalized.ID != "" {
			activeSession.Participants[i] = normalized
			return true
		}
	}
	activeSession.Participants = append(activeSession.Participants, normalized)
	return true
}

// RemoveParticipantBinding removes participant bindings matching participantID
// from a session. A non-empty id returns true even when no binding matched so
// callers keep the historical "detach requested" document update semantics.
func RemoveParticipantBinding(activeSession *Session, participantID string) bool {
	if activeSession == nil {
		return false
	}
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return false
	}
	filtered := activeSession.Participants[:0]
	for _, item := range activeSession.Participants {
		if strings.TrimSpace(item.ID) == participantID {
			continue
		}
		filtered = append(filtered, item)
	}
	activeSession.Participants = append([]ParticipantBinding(nil), filtered...)
	return true
}
