package local

import (
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestParticipantPromptUserEventUsesDisplayInputForProjection(t *testing.T) {
	t.Parallel()

	modelInput := "Load skill `cmpctl` before taking task actions, then follow its instructions.\n\nUser request:\narchive preflight"
	displayInput := "$cmpctl archive preflight"
	event := participantPromptUserEvent(
		session.Session{Controller: session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "kernel-1"}},
		session.ParticipantBinding{ID: "p-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar, AgentName: "helper"},
		"turn-1",
		"test",
		modelInput,
		displayInput,
		"",
		nil,
		time.Unix(1, 0),
	)
	if event == nil {
		t.Fatal("participantPromptUserEvent() = nil")
	}
	if event.Message == nil || event.Message.TextContent() != modelInput {
		t.Fatalf("event.Message = %#v, want model-visible input", event.Message)
	}
	if event.Text != displayInput {
		t.Fatalf("event.Text = %q, want display input %q", event.Text, displayInput)
	}
	if got := event.Meta["display_input"]; got != displayInput {
		t.Fatalf("event.Meta[display_input] = %#v, want %q", got, displayInput)
	}
	update := session.ProtocolUpdateOf(event)
	content, _ := update.Content.(map[string]any)
	if content["text"] != displayInput {
		t.Fatalf("protocol content = %#v, want display input", update.Content)
	}
}
