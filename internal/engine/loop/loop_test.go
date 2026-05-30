package loop

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

func TestLoopPassesConfiguredInstructionsToProvider(t *testing.T) {
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("pong")},
	}}
	runner, err := New(Config{
		Provider:     provider,
		Instructions: []string{" system rule ", "", "workspace rule"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := runner.Run(context.Background(), Request{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-1"}},
		Input:   "ping",
		TurnID:  "turn-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want user and assistant", len(events))
	}
	if len(provider.request.Instructions) != 2 || provider.request.Instructions[0] != "system rule" || provider.request.Instructions[1] != "workspace rule" {
		t.Fatalf("instructions = %#v, want trimmed configured instructions", provider.request.Instructions)
	}
}

type capturingProvider struct {
	request model.Request
	message model.Message
}

func (p *capturingProvider) ID() string {
	return "capturing"
}

func (p *capturingProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "capturing", Provider: "capturing"}}, nil
}

func (p *capturingProvider) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	p.request = model.Request{
		Model:        req.Model,
		Messages:     cloneTestMessages(req.Messages),
		Tools:        req.Tools,
		Instructions: append([]string(nil), req.Instructions...),
		Stream:       req.Stream,
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: model.CloneMessage(p.message),
		},
	}}}, nil
}

func cloneTestMessages(in []model.Message) []model.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.Message, 0, len(in))
	for _, message := range in {
		out = append(out, model.CloneMessage(message))
	}
	return out
}
