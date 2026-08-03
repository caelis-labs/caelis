package runtime

import (
	"context"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestValidateRunInputKeepsAgentContextTyped(t *testing.T) {
	t.Parallel()

	valid := agent.RunRequest{
		Input: "status", InputType: session.EventTypeContext, InputMessageID: "message-1",
		InputActor: session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1"},
	}
	if err := validateRunInput(valid); err != nil {
		t.Fatalf("validateRunInput(valid) error = %v", err)
	}
	for name, req := range map[string]agent.RunRequest{
		"missing identity": {Input: "status", InputType: session.EventTypeContext, InputMessageID: "message-1"},
		"missing message id": {
			Input: "status", InputType: session.EventTypeContext,
			InputActor: session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1"},
		},
		"unsupported event": {Input: "status", InputType: session.EventTypeAssistant},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRunInput(req); err == nil {
				t.Fatalf("validateRunInput(%#v) error = nil", req)
			}
		})
	}
}

func TestSendAgentMessageToIdleMainPersistsContextWithoutStartingRun(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	response, err := runtime.SendAgentMessage(context.Background(), activeSession.SessionRef, agentmessage.Request{
		MessageID: "idle-parent-message-1", To: agentmessage.Parent, Text: "child status",
		From: session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1", Name: "@orbit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Accepted || response.State != "pending" {
		t.Fatalf("response = %#v, want accepted pending", response)
	}
	if active := runtime.activeRunForSession(activeSession.SessionRef); active.handle != nil {
		t.Fatalf("idle delivery started active run %#v", active)
	}
	events, err := runtime.sessions.Events(context.Background(), session.EventsRequest{SessionRef: activeSession.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var message *session.Event
	for _, event := range events {
		if event != nil && event.MessageID == "idle-parent-message-1" {
			message = event
		}
	}
	if message == nil || session.EventTypeOf(message) != session.EventTypeContext || message.Actor.Kind != session.ActorKindParticipant {
		t.Fatalf("persisted Agent message = %#v", message)
	}
	if strings.TrimSpace(message.Text) != "child status" {
		t.Fatalf("persisted text = %q", message.Text)
	}
}

func TestSendAgentMessageRetryDoesNotResubmitLiveContext(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	handle := newRunner("agent-message-run", func() {})
	runtime.registerActiveRun(activeSession.SessionRef, activeSession, "turn-1", handle)
	defer runtime.unregisterActiveRun(handle.RunID())

	req := agentmessage.Request{
		MessageID: "retry-parent-message-1", To: agentmessage.Parent, Text: "child status",
		From: session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1", Name: "@orbit"},
	}
	first, err := runtime.SendAgentMessage(context.Background(), activeSession.SessionRef, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.SendAgentMessage(context.Background(), activeSession.SessionRef, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "delivered" || second.State != "pending" {
		t.Fatalf("delivery states = %q, %q; want delivered then idempotent pending", first.State, second.State)
	}
	submissions := handle.drainSubmissions()
	if len(submissions) != 1 || submissions[0].MessageID != req.MessageID {
		t.Fatalf("live submissions = %#v, want one logical Agent message", submissions)
	}
	events, err := runtime.sessions.Events(context.Background(), session.EventsRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	var matches int
	for _, event := range events {
		if event != nil && event.MessageID == req.MessageID {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("durable Agent messages = %d, want one", matches)
	}
}
