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
	provider := &recordingReviewProvider{
		response: `{"risk_level":"high","user_authorization":"unknown","outcome":"deny","rationale":"not authorized"}`,
		usage:    &model.Usage{InputTokens: 12, OutputTokens: 3, TotalTokens: 15},
	}
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
	usage, _ := decision.Meta["usage"].(map[string]any)
	review, _ := decision.Meta["approval_review"].(map[string]any)
	if decision.Meta["usage_category"] != "auto_review" ||
		usage["total_tokens"] != 15 ||
		review["risk_level"] != "high" ||
		review["user_authorization"] != "unknown" ||
		review["outcome"] != "deny" {
		t.Fatalf("decision meta = %#v, want review usage and denial metadata", decision.Meta)
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

func TestWithModelReviewReusesValidatedTranscriptPrefix(t *testing.T) {
	provider := &recordingReviewProvider{responses: []string{
		`{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"first ok"}`,
		`{"risk_level":"high","user_authorization":"unknown","outcome":"deny","rationale":"second denied"}`,
		`{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"third ok"}`,
	}}
	policy := WithModelReview(AskTools("write_file"), provider)
	firstRaw := json.RawMessage(`{"path":"first.txt","content":"x"}`)
	firstEvents := []session.Event{{
		Type:    session.EventUser,
		Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("first request")}},
	}}

	first, err := policy.ReviewToolCall(context.Background(), Request{
		Mode:   ModeAutoReview,
		Model:  "review-model",
		Events: firstEvents,
		Call:   model.ToolCall{Name: "write_file", Input: firstRaw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Verdict != VerdictAllow {
		t.Fatalf("first decision = %#v, want allow", first)
	}
	prefix, ok := first.Meta["approval_review_prefix"].(map[string]any)
	messages, _ := prefix["messages"].([]map[string]any)
	if !ok || prefix["prompt"] == "" || prefix["assessment"] == "" || len(messages) != 2 {
		t.Fatalf("first meta = %#v, want reusable prefix", first.Meta)
	}
	secondRaw := json.RawMessage(`{"path":"second.txt","content":"x"}`)
	secondEvents := append([]session.Event{}, firstEvents...)
	secondEvents = append(secondEvents,
		session.Event{Type: session.EventApproval, Meta: first.Meta},
		session.Event{Type: session.EventUser, Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("second request")}}},
	)

	second, err := policy.ReviewToolCall(context.Background(), Request{
		Mode:   ModeAutoReview,
		Model:  "review-model",
		Events: secondEvents,
		Call:   model.ToolCall{Name: "write_file", Input: secondRaw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Verdict != VerdictDeny || second.Reason != "second denied" {
		t.Fatalf("second decision = %#v, want denial", second)
	}
	if len(provider.requests) != 2 || len(provider.requests[1].Messages) != 3 {
		t.Fatalf("second review messages = %#v, want reusable prefix plus delta", provider.requests)
	}
	if got, want := provider.requests[1].Messages[0].TextContent(), provider.requests[0].Messages[0].TextContent(); got != want {
		t.Fatalf("reused prefix prompt = %q, want first prompt %q", got, want)
	}
	if !strings.Contains(provider.requests[1].Messages[1].TextContent(), `"outcome": "allow"`) {
		t.Fatalf("prefix assessment = %q, want first validated assessment", provider.requests[1].Messages[1].TextContent())
	}
	delta := provider.requests[1].Messages[2].TextContent()
	if !strings.Contains(delta, "Transcript delta") || !strings.Contains(delta, "second request") || strings.Contains(delta, "first request") {
		t.Fatalf("delta prompt = %q, want only new transcript entries", delta)
	}
	thirdRaw := json.RawMessage(`{"path":"third.txt","content":"x"}`)
	thirdEvents := append([]session.Event{}, secondEvents...)
	thirdEvents = append(thirdEvents,
		session.Event{Type: session.EventApproval, Meta: second.Meta},
		session.Event{Type: session.EventUser, Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("third request")}}},
	)

	third, err := policy.ReviewToolCall(context.Background(), Request{
		Mode:   ModeAutoReview,
		Model:  "review-model",
		Events: thirdEvents,
		Call:   model.ToolCall{Name: "write_file", Input: thirdRaw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if third.Verdict != VerdictAllow {
		t.Fatalf("third decision = %#v, want allow", third)
	}
	if len(provider.requests) != 3 || len(provider.requests[2].Messages) != 5 {
		t.Fatalf("third review messages = %#v, want cumulative reusable prefix plus delta", provider.requests)
	}
	if got := provider.requests[2].Messages[0].TextContent(); !strings.Contains(got, "first request") {
		t.Fatalf("third prefix first prompt = %q, want first transcript retained", got)
	}
	if got := provider.requests[2].Messages[2].TextContent(); !strings.Contains(got, "second request") || strings.Contains(got, "third request") {
		t.Fatalf("third prefix second prompt = %q, want prior delta retained", got)
	}
	thirdDelta := provider.requests[2].Messages[4].TextContent()
	if !strings.Contains(thirdDelta, "third request") || strings.Contains(thirdDelta, "first request") || strings.Contains(thirdDelta, "second request") {
		t.Fatalf("third delta prompt = %q, want only latest transcript entry", thirdDelta)
	}
}

type recordingReviewProvider struct {
	request   model.Request
	requests  []model.Request
	response  string
	responses []string
	usage     *model.Usage
	calls     int
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
	p.requests = append(p.requests, req)
	response := p.response
	if len(p.responses) > 0 {
		response = p.responses[0]
		p.responses = p.responses[1:]
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status: model.ResponseCompleted,
			Message: model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart(response)},
			},
			Usage: p.usage,
		},
	}}}, nil
}
