package sessiontest

import (
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestParticipantEventMatchesRejectsUnrelatedAssistantEvent(t *testing.T) {
	t.Parallel()
	binding := session.ParticipantBinding{
		ID: "participant-a", Kind: session.ParticipantKindSubagent,
		Role: session.ParticipantRoleSidecar, DelegationID: "delegation-a",
	}
	requested := participantEvent("attach-a", binding, "attached")
	actual := &session.Event{
		ID: "event-a", IdempotencyKey: requested.IdempotencyKey,
		SessionID: "session-a", Seq: 1, Schema: session.EventSchemaVersion,
		Type: session.EventTypeAssistant, Visibility: requested.Visibility, Time: time.Now(),
	}
	if participantEventMatches(session.SessionRef{SessionID: "session-a"}, requested, actual) {
		t.Fatal("unrelated assistant event passed participant lifecycle validation")
	}
}

func TestParticipantJSONEqualRejectsCorruptedReturnedSessionTitle(t *testing.T) {
	t.Parallel()
	durable := session.Session{SessionRef: session.SessionRef{SessionID: "session-a"}, Revision: 2, Title: "durable title"}
	returned := session.CloneSession(durable)
	returned.Title = "corrupted title"
	if participantJSONEqual(returned, durable) {
		t.Fatal("corrupted returned Session.Title passed whole-object validation")
	}
}
