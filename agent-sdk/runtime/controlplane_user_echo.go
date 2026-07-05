package runtime

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

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
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp")
}

func isACPParticipantUserEcho(event *session.Event) bool {
	if event == nil || event.Scope == nil {
		return false
	}
	if session.EventTypeOf(event) != session.EventTypeUser {
		return false
	}
	if strings.TrimSpace(event.Scope.Participant.ID) == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.Scope.Source)), "acp")
}
