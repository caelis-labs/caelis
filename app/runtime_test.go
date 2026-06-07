package app

import (
	"context"
	"iter"
	"testing"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/gateway"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
)

func TestNewRuntimeProvidesGatewayTurnPath(t *testing.T) {
	rt, err := NewRuntime(RuntimeConfig{Agent: staticAgent{name: "test-agent"}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	if rt.Gateway == nil {
		t.Fatal("Gateway is nil")
	}

	ctx := context.Background()
	sess, err := rt.Gateway.CreateSession(ctx, gateway.CreateSessionRequest{
		AppName: "app", UserID: "u", WorkspaceKey: "ws",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	turn, err := rt.Gateway.BeginTurn(ctx, gateway.TurnRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if turn.TurnID == "" {
		t.Fatal("TurnID is empty")
	}

	err = rt.Gateway.Submit(ctx, gateway.SubmitRequest{
		TurnID:      turn.TurnID,
		UserMessage: model.Message{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	replay, err := rt.Gateway.Replay(ctx, gateway.ReplayRequest{SessionRef: sess.Ref})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replay.Events) != 2 {
		t.Fatalf("replay events = %d, want 2", len(replay.Events))
	}
	if replay.Events[0].Kind != string(session.EventKindUser) {
		t.Fatalf("event 0 kind = %q", replay.Events[0].Kind)
	}
	if replay.Events[1].Kind != string(session.EventKindAssistant) {
		t.Fatalf("event 1 kind = %q", replay.Events[1].Kind)
	}
}

type staticAgent struct {
	name string
}

func (a staticAgent) Name() string {
	return a.name
}

func (a staticAgent) Description() string {
	return ""
}

func (a staticAgent) SubAgents() []agent.Agent {
	return nil
}

func (a staticAgent) FindAgent(string) agent.Agent {
	return nil
}

func (a staticAgent) Run(agent.InvocationContext) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		yield(session.Event{
			Kind:       session.EventKindAssistant,
			Visibility: session.VisibilityCanonical,
			AssistantPayload: &session.AssistantPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "ok"}},
			},
		}, nil)
	}
}
