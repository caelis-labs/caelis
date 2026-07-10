package runtime

import "github.com/caelis-labs/caelis/agent-sdk/session"

func isACPControllerUserEcho(event *session.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if session.EventTypeOf(event) != session.EventTypeUser {
		return false
	}
	if event.Scope.Participant.ID != "" {
		return false
	}
	return event.Scope.Controller.Kind == session.ControllerKindACP
}

func isACPParticipantUserEcho(event *session.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if session.EventTypeOf(event) != session.EventTypeUser {
		return false
	}
	if event.Scope.Participant.ID == "" {
		return false
	}
	return event.Scope.Participant.Kind == session.ParticipantKindACP
}
