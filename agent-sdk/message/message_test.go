package message

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	memory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestToolMessageIDIsStableAndSessionScoped(t *testing.T) {
	t.Parallel()

	first, err := ToolMessageID(session.SessionRef{SessionID: "session-1"}, "call-1")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := ToolMessageID(session.SessionRef{SessionID: "session-1"}, " call-1 ")
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := ToolMessageID(session.SessionRef{SessionID: "session-2"}, "call-1")
	if err != nil {
		t.Fatal(err)
	}
	otherCall, err := ToolMessageID(session.SessionRef{SessionID: "session-1"}, "call-2")
	if err != nil {
		t.Fatal(err)
	}
	if first != retry || first == otherSession || first == otherCall || !strings.HasPrefix(first, toolMessageIDPrefix) {
		t.Fatalf("message ids = %q, %q, %q, %q; want stable session/call-scoped identity", first, retry, otherSession, otherCall)
	}
	for _, test := range []struct {
		name string
		ref  session.SessionRef
		call string
	}{
		{name: "missing session", call: "call-1"},
		{name: "missing call", ref: session.SessionRef{SessionID: "session-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ToolMessageID(test.ref, test.call); err == nil {
				t.Fatal("ToolMessageID() error = nil, want invalid identity rejection")
			}
		})
	}
}

func TestContextEventMergesTargetScopeAndAgentProvenance(t *testing.T) {
	t.Parallel()

	event, err := ContextEvent(Request{
		MessageID: "message-1", To: "parent", Text: "status",
		From:  session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1", Name: "@orbit"},
		Scope: &session.EventScope{Participant: session.ParticipantRef{ID: "child-1"}},
	}, session.EventScope{
		TurnID: "turn-1", Controller: session.ControllerRef{Kind: session.ControllerKindKernel, ID: "kernel-1"},
		Executor: session.ActorRef{Kind: session.ActorKindController, ID: "kernel-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != session.EventTypeContext || event.Visibility != session.VisibilityCanonical ||
		event.Scope == nil || event.Scope.Source != "agent_message" || event.Scope.TurnID != "turn-1" {
		t.Fatalf("Context event = %#v, want canonical target scope", event)
	}
	if event.Actor.Kind != session.ActorKindParticipant || event.Actor.ID != "child-1" || event.Meta["agent_message"] != true {
		t.Fatalf("Context provenance = %#v / %#v, want typed Agent source", event.Actor, event.Meta)
	}
	if message, ok := session.ModelMessageOf(event); !ok || message.Role != "user" || !strings.Contains(event.Text, "status") {
		t.Fatalf("provider message = %#v, %v", message, ok)
	}
}

func TestAppendContextRequiresAtomicAppendOutcome(t *testing.T) {
	t.Parallel()

	base := memory.NewStore(memory.Config{})
	active, err := base.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "message"})
	if err != nil {
		t.Fatal(err)
	}
	service := eventAppenderOnly{EventAppender: base}
	_, err = AppendContext(context.Background(), service, active.SessionRef, session.MutationGuard{}, session.EventScope{}, Request{
		MessageID: "message-1", To: "parent", Text: "status",
		From: session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1"},
	})
	if err == nil || !strings.Contains(err.Error(), "EventAppenderWithOutcome") {
		t.Fatalf("AppendContext() error = %v, want precise outcome requirement", err)
	}
}

func TestAppendContextDeduplicatesStableMessageAndRejectsChangedPayload(t *testing.T) {
	t.Parallel()

	store := memory.NewStore(memory.Config{})
	active, err := store.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "message"})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		MessageID: "message-1", To: "parent", Text: "status",
		From: session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1"},
	}
	first, err := AppendContext(context.Background(), store, active.SessionRef, session.MutationGuard{}, session.EventScope{}, req)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := AppendContext(context.Background(), store, active.SessionRef, session.MutationGuard{}, session.EventScope{}, req)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Appended || retry.Appended || first.Event == nil || retry.Event == nil || first.Event.ID != retry.Event.ID {
		t.Fatalf("append outcomes = %#v / %#v, want one canonical event", first, retry)
	}

	req.Text = "changed status"
	_, err = AppendContext(context.Background(), store, active.SessionRef, session.MutationGuard{}, session.EventScope{}, req)
	var conflict *session.EventConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("changed retry error = %v, want EventConflictError", err)
	}
}

type eventAppenderOnly struct{ session.EventAppender }
