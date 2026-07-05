package runtime

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestIsACPControllerUserEcho(t *testing.T) {
	t.Parallel()

	echo := &session.Event{
		Type: session.EventTypeUser,
		Scope: &session.EventScope{
			Source: "acp",
		},
	}
	if !isACPControllerUserEcho(echo) {
		t.Fatal("isACPControllerUserEcho() = false, want true for controller echo")
	}
	participant := &session.Event{
		Type: session.EventTypeUser,
		Scope: &session.EventScope{
			Source:      "acp",
			Participant: session.ParticipantRef{ID: "emma"},
		},
	}
	if isACPControllerUserEcho(participant) {
		t.Fatal("isACPControllerUserEcho() = true, want false for participant user")
	}
}
