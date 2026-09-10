package session

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
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
