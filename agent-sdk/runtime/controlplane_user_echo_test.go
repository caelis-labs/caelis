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
			Source:     "arbitrary-audit-origin",
			Controller: session.ControllerRef{Kind: session.ControllerKindACP},
		},
	}
	if !isACPControllerUserEcho(echo) {
		t.Fatal("isACPControllerUserEcho() = false, want true for controller echo")
	}
	participant := &session.Event{
		Type: session.EventTypeUser,
		Scope: &session.EventScope{
			Source:      "acp-looking-but-irrelevant",
			Controller:  session.ControllerRef{Kind: session.ControllerKindACP},
			Participant: session.ParticipantRef{ID: "emma", Kind: session.ParticipantKindACP},
		},
	}
	if isACPControllerUserEcho(participant) {
		t.Fatal("isACPControllerUserEcho() = true, want false for participant user")
	}
	if !isACPParticipantUserEcho(participant) {
		t.Fatal("isACPParticipantUserEcho() = false, want ACP participant kind to control echo")
	}
	participant.Scope.Source = "not-acp"
	if !isACPParticipantUserEcho(participant) {
		t.Fatal("changing audit Source changed participant echo behavior")
	}
	participant.Scope.Participant.Kind = session.ParticipantKindSubagent
	participant.Scope.Source = "acp"
	if isACPParticipantUserEcho(participant) {
		t.Fatal("ACP-looking Source changed non-ACP participant behavior")
	}
}
