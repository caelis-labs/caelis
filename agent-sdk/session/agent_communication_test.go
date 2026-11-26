package session

import (
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"strings"
	"testing"
)

func TestParentCommunicationActorUsesReservedHandleNotLocal(t *testing.T) {
	t.Parallel()

	got := ParentCommunicationActor()
	want := ActorRef{Kind: ActorKindController, ID: AgentCommunicationParentHandle, Name: AgentCommunicationParentHandle}
	if got != want {
		t.Fatalf("ParentCommunicationActor() = %#v, want reserved parent identity %#v", got, want)
	}
	executor := ControllerExecutor(ControllerBinding{
		Kind: ControllerKindKernel, ControllerID: "sdk-kernel", AgentName: "local",
	})
	if executor.ID == got.ID || strings.EqualFold(executor.Name, got.Name) {
		t.Fatalf("controller executor %#v must stay distinct from recipient-visible parent %#v", executor, got)
	}
}

func TestAgentCommunicationPromptFooterUsesParentNotLocalControllerName(t *testing.T) {
	t.Parallel()

	header := AgentCommunicationPromptFooter(ControllerExecutor(ControllerBinding{
		Kind: ControllerKindKernel, ControllerID: "sdk-kernel", AgentName: "local", Label: "SDK Kernel",
	}))
	if !strings.Contains(header, "From: parent") {
		t.Fatalf("parent prompt = %q, want canonical parent sender", header)
	}
	if strings.Contains(header, "Reply-To:") {
		t.Fatalf("parent prompt added Reply-To: %q", header)
	}
	for _, leaked := range []string{"local", "sdk-kernel", "kernel", "SDK Kernel"} {
		if strings.Contains(header, leaked) {
			t.Fatalf("parent prompt leaked controller product identity %q: %q", leaked, header)
		}
	}
}

func TestAgentCommunicationPromptFooterKeepsParticipantSender(t *testing.T) {
	t.Parallel()

	header := AgentCommunicationPromptFooter(ActorRef{
		Kind: ActorKindParticipant, ID: "reviewer-1", Role: "delegated", Name: "reviewer",
	})
	if header != "\n\nFrom: reviewer" {
		t.Fatalf("participant prompt = %q, want reviewer identity", header)
	}
}

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
		if communication == nil || communication.Text != "" || IsCanonicalHistoryEvent(observed) {
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
