package session

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// ParticipantBindingConflictError reports an attempted overwrite or removal
// of a participant ID owned by a different delegation.
type ParticipantBindingConflictError struct {
	ParticipantID      string
	ExpectedDelegation string
	ActualDelegation   string
}

func (e *ParticipantBindingConflictError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("agent-sdk/session: participant %q delegation conflict: expected %q, actual %q",
		strings.TrimSpace(e.ParticipantID), strings.TrimSpace(e.ExpectedDelegation), strings.TrimSpace(e.ActualDelegation))
}

func (e *ParticipantBindingConflictError) ErrorCode() errorcode.Code { return errorcode.Conflict }

// CheckParticipantDelegation rejects a participant identity collision. A
// missing participant is safe for idempotent attach/detach recovery.
func CheckParticipantDelegation(activeSession *Session, participantID, expectedDelegation string) error {
	if activeSession == nil {
		return nil
	}
	participantID = strings.TrimSpace(participantID)
	expectedDelegation = strings.TrimSpace(expectedDelegation)
	for _, binding := range activeSession.Participants {
		if strings.TrimSpace(binding.ID) != participantID {
			continue
		}
		actual := strings.TrimSpace(binding.DelegationID)
		if actual != expectedDelegation {
			return &ParticipantBindingConflictError{
				ParticipantID: participantID, ExpectedDelegation: expectedDelegation, ActualDelegation: actual,
			}
		}
		return nil
	}
	return nil
}

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
