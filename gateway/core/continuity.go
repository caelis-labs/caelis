package core

import (
	"strings"

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func buildContinuityState(session sdksession.Session, events []*sdksession.Event) ContinuityState {
	state := ContinuityState{}
	if len(events) == 0 {
		return state
	}
	participantCursors := map[string]string{}
	controllerEpoch := strings.TrimSpace(session.Controller.EpochID)
	for _, event := range events {
		if event == nil || strings.TrimSpace(event.ID) == "" {
			continue
		}
		state.LastEventCursor = event.ID
		if scope := event.Scope; scope != nil {
			if controllerEpoch != "" && strings.TrimSpace(scope.Controller.EpochID) == controllerEpoch {
				state.ControllerCursor = event.ID
			}
			if participantID := strings.TrimSpace(scope.Participant.ID); participantID != "" {
				participantCursors[participantID] = event.ID
			}
			if acpSessionID := strings.TrimSpace(scope.ACP.SessionID); acpSessionID != "" {
				state.ACPProjection = ACPProjectionState{
					Cursor:    event.ID,
					SessionID: acpSessionID,
					EventType: strings.TrimSpace(scope.ACP.EventType),
				}
			}
		}
	}
	if len(participantCursors) > 0 {
		state.ParticipantCursors = participantCursors
	}
	return state
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
