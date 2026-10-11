package session

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestAgentCommunicationObservedClearDoesNotRelaxCanonicalInput(t *testing.T) {
	message := model.NewTextMessage(model.RoleUser, "admitted message")
	protocol := NewAgentCommunicationProtocol(ProtocolAgentCommunication{Text: "admitted message"})
	protocol.Update.MessageID = "mail"
	admitted := &Event{Type: EventTypeContext, Visibility: VisibilityCanonical, Actor: ParentCommunicationActor(), Message: &message, Text: "admitted message", Protocol: &protocol}
	if err := ValidateDurableCoreEvent(admitted); err != nil {
		t.Fatal(err)
	}
	for _, visibility := range []Visibility{VisibilityUIOnly, VisibilityMirror} {
		observed := CloneEvent(admitted)
		observed.Visibility, observed.Text = visibility, ""
		empty := model.NewTextMessage(model.RoleUser, "")
		observed.Message = &empty
		observed.Protocol.Update.Content = map[string]any{"type": "text", "text": ""}
		observed.Protocol.Update.Replacement = true
		communication := ProtocolAgentCommunicationOf(observed)
		if communication == nil || communication.Text != "" || IsInvocationVisibleEvent(observed) {
			t.Fatalf("display clear lost or acquired model authority: %#v", observed)
		}
		canonical := CloneEvent(observed)
		canonical.Visibility = VisibilityCanonical
		if ProtocolAgentCommunicationOf(canonical) != nil || ValidateDurableCoreEvent(canonical) == nil {
			t.Fatal("empty display repair relaxed canonical input validation")
		}
		observed.Actor = ActorRef{Kind: ActorKindUser, Name: "user"}
		if IsClientReplayEvent(observed) {
			t.Fatal("display repair relaxed typed source validation")
		}
	}
	actual, ok := ModelMessageOf(admitted)
	if !ok || actual.TextContent() != "admitted message" || EventText(admitted) != "admitted message" {
		t.Fatal("observed clear changed admitted model input")
	}
}
