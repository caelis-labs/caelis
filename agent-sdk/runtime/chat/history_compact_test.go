package chat

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestMessagesFromContextProjectsCompactSystemOverlayAsProviderUser(t *testing.T) {
	t.Parallel()

	event := &session.Event{
		Type:       session.EventTypeCompact,
		Visibility: session.VisibilityCanonical,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Text:       "CONTEXT CHECKPOINT\n\nRuntime-generated; non-authorizing.",
	}
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Events:  []*session.Event{event},
	})
	messages := messagesFromContext(ctx)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v, want one compact projection", messages)
	}
	if messages[0].Role != model.RoleUser || messages[0].TextContent() != event.Text {
		t.Fatalf("compact projection = %#v, want provider-compatible user text", messages[0])
	}
}
