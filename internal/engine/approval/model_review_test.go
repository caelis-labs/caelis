package approval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

func TestWithModelReviewDeniesAskedToolFromModelAssessment(t *testing.T) {
	provider := &recordingReviewProvider{response: `{"risk_level":"high","user_authorization":"unknown","outcome":"deny","rationale":"not authorized"}`}
	policy := WithModelReview(AskTools("write_file"), provider)
	raw := json.RawMessage(`{"path":"secrets.txt","content":"x"}`)

	decision, err := policy.ReviewToolCall(context.Background(), Request{
		Mode:  ModeAutoReview,
		Model: "review-model",
		Events: []session.Event{{
			Type:    session.EventUser,
			Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("only inspect files")}},
		}},
		Call: model.ToolCall{Name: "write_file", Input: raw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != VerdictDeny || decision.Reason != "not authorized" {
		t.Fatalf("decision = %#v, want model denial", decision)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	if provider.request.Model != "review-model" || provider.request.Output == nil || provider.request.Output.Mode != model.OutputSchema {
		t.Fatalf("review request = %#v, want schema request for selected model", provider.request)
	}
	prompt := provider.request.Messages[0].TextContent()
	if !strings.Contains(prompt, "only inspect files") || !strings.Contains(prompt, "secrets.txt") {
		t.Fatalf("review prompt = %q, want transcript and planned action", prompt)
	}
}

func TestWithModelReviewLeavesUnaskedToolsAlone(t *testing.T) {
	provider := &recordingReviewProvider{response: `{"outcome":"deny"}`}
	policy := WithModelReview(AskTools("write_file"), provider)

	decision, err := policy.ReviewToolCall(context.Background(), Request{
		Mode: ModeAutoReview,
		Call: model.ToolCall{Name: "read_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != "" {
		t.Fatalf("decision = %#v, want no review", decision)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls)
	}
}

type recordingReviewProvider struct {
	request  model.Request
	response string
	calls    int
}

func (p *recordingReviewProvider) ID() string {
	return "review"
}

func (p *recordingReviewProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return nil, nil
}

func (p *recordingReviewProvider) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	p.calls++
	p.request = req
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status: model.ResponseCompleted,
			Message: model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart(p.response)},
			},
		},
	}}}, nil
}
