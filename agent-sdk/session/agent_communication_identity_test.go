package session

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestAgentCommunicationMarkerCannotReclassifyUserHistory(t *testing.T) {
	t.Parallel()

	for _, eventType := range []EventType{"", EventTypeUser} {
		message := model.NewTextMessage(model.RoleUser, "remote input")
		protocol := NewAgentCommunicationProtocol(ProtocolAgentCommunication{Text: "remote input"})
		event := &Event{Type: eventType, Visibility: VisibilityCanonical,
			Actor:   ActorRef{Kind: ActorKindUser, Name: "user"},
			Message: &message, Text: "remote input", Protocol: &protocol,
		}
		if EventTypeOf(event) != EventTypeUser || IsAgentCommunicationProtocol(event) {
			t.Fatalf("peer marker reclassified user history: %#v", event)
		}
		if err := ValidateDurableCoreEvent(event); err != nil {
			t.Fatal(err)
		}
		if !IsClientReplayEvent(event) {
			t.Fatalf("peer marker hid user history: %#v", event)
		}
	}
}
