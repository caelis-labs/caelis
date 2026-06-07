package autoreview

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
)

func TestRequesterAllowsLowRiskModelDecision(t *testing.T) {
	llm := &scriptedReviewLLM{events: []model.ResponseEvent{{TextDelta: `{"outcome":"allow"}`}}}
	requester := New(Config{Model: llm})

	resp, err := requester.RequestApproval(context.Background(), agent.ApprovalRequest{
		ToolName: "WRITE",
		CallID:   "call-123",
		Args:     map[string]any{"path": "/tmp/out.txt", "content": "hello"},
		Reason:   "policy requires approval",
		RunID:    "run-456",
	})
	if err != nil {
		t.Fatalf("RequestApproval error = %v", err)
	}
	if !resp.Approved {
		t.Fatalf("approved = false, want true: %#v", resp)
	}
	if !strings.Contains(resp.Reason, "low-risk allow") {
		t.Fatalf("reason = %q, want low-risk default rationale", resp.Reason)
	}

	prompt := llm.lastRequest.Messages[0].TextContent()
	if strings.Contains(prompt, "call-123") || strings.Contains(prompt, "run-456") {
		t.Fatalf("prompt leaked volatile ids:\n%s", prompt)
	}
	if !strings.Contains(prompt, `"tool": "WRITE"`) || !strings.Contains(prompt, `"path": "/tmp/out.txt"`) {
		t.Fatalf("prompt missing planned action:\n%s", prompt)
	}
	if llm.lastRequest.Output == nil || llm.lastRequest.Output.Mode != model.OutputModeSchema {
		t.Fatalf("output spec = %#v, want schema mode", llm.lastRequest.Output)
	}
}

func TestRequesterDeniesModelDecisionWithRationale(t *testing.T) {
	llm := &scriptedReviewLLM{events: []model.ResponseEvent{{TextDelta: `{"risk_level":"high","user_authorization":"unknown","outcome":"deny","rationale":"target not authorized"}`}}}
	requester := New(Config{Model: llm})

	resp, err := requester.RequestApproval(context.Background(), agent.ApprovalRequest{
		ToolName: "RUN_COMMAND",
		Args:     map[string]any{"cmd": "rm -rf /tmp/project"},
	})
	if err != nil {
		t.Fatalf("RequestApproval error = %v", err)
	}
	if resp.Approved {
		t.Fatal("approved = true, want false")
	}
	if resp.Reason != "target not authorized" {
		t.Fatalf("reason = %q", resp.Reason)
	}
}

func TestRequesterRejectsInvalidModelDecision(t *testing.T) {
	llm := &scriptedReviewLLM{events: []model.ResponseEvent{{TextDelta: `{"outcome":"maybe"}`}}}
	requester := New(Config{Model: llm})

	_, err := requester.RequestApproval(context.Background(), agent.ApprovalRequest{ToolName: "WRITE"})
	if err == nil {
		t.Fatal("expected invalid decision error")
	}
	if !strings.Contains(err.Error(), "unsupported outcome") {
		t.Fatalf("error = %v", err)
	}
}

func TestRequesterUsesStableTranscriptDeltaAcrossRequests(t *testing.T) {
	llm := &scriptedReviewLLM{events: []model.ResponseEvent{{TextDelta: `{"outcome":"allow"}`}}}
	requester := New(Config{Model: llm})
	sess := session.Session{
		Ref: session.Ref{AppName: "test", UserID: "user", WorkspaceKey: "workspace", SessionID: "sess-1"},
	}
	transcript := []session.Event{
		userEvent("event-1", "first user request"),
		assistantEvent("event-2", "first assistant response"),
	}
	if _, err := requester.RequestApproval(context.Background(), agent.ApprovalRequest{
		ToolName:   "WRITE",
		Session:    sess,
		Transcript: transcript,
	}); err != nil {
		t.Fatalf("first RequestApproval error = %v", err)
	}

	transcript = append(transcript, userEvent("event-3", "second user request"))
	if _, err := requester.RequestApproval(context.Background(), agent.ApprovalRequest{
		ToolName:   "WRITE",
		Session:    sess,
		Transcript: transcript,
	}); err != nil {
		t.Fatalf("second RequestApproval error = %v", err)
	}

	if len(llm.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(llm.requests))
	}
	firstPrompt := llm.requests[0].Messages[0].TextContent()
	if !strings.Contains(firstPrompt, "TRANSCRIPT START") || !strings.Contains(firstPrompt, "first user request") {
		t.Fatalf("first prompt missing full transcript:\n%s", firstPrompt)
	}
	secondPrompt := llm.requests[1].Messages[0].TextContent()
	if !strings.Contains(secondPrompt, "TRANSCRIPT DELTA START") {
		t.Fatalf("second prompt missing delta marker:\n%s", secondPrompt)
	}
	if strings.Contains(secondPrompt, "first user request") || !strings.Contains(secondPrompt, "second user request") {
		t.Fatalf("second prompt did not use transcript delta:\n%s", secondPrompt)
	}
}

func TestRequesterRequiresModel(t *testing.T) {
	_, err := New(Config{}).RequestApproval(context.Background(), agent.ApprovalRequest{ToolName: "WRITE"})
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("error = %v, want missing model", err)
	}
}

type scriptedReviewLLM struct {
	events      []model.ResponseEvent
	lastRequest model.Request
	requests    []model.Request
}

func (m *scriptedReviewLLM) Name() string { return "scripted-reviewer" }

func (m *scriptedReviewLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	m.lastRequest = cloneReviewRequest(req)
	m.requests = append(m.requests, m.lastRequest)
	return func(yield func(model.ResponseEvent, error) bool) {
		for _, event := range m.events {
			if !yield(event, nil) {
				return
			}
		}
	}
}

func userEvent(id string, text string) session.Event {
	return session.Event{
		ID:         id,
		Kind:       session.EventKindUser,
		Visibility: session.VisibilityCanonical,
		CreatedAt:  time.Now(),
		UserPayload: &session.UserPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: text}},
		},
	}
}

func assistantEvent(id string, text string) session.Event {
	return session.Event{
		ID:         id,
		Kind:       session.EventKindAssistant,
		Visibility: session.VisibilityCanonical,
		CreatedAt:  time.Now(),
		AssistantPayload: &session.AssistantPayload{
			Parts: []session.EventPart{{Kind: session.PartKindText, Text: text}},
		},
	}
}

func cloneReviewRequest(req model.Request) model.Request {
	out := req
	out.Messages = make([]model.Message, len(req.Messages))
	for i, msg := range req.Messages {
		out.Messages[i] = msg.Clone()
	}
	if req.Output != nil {
		output := *req.Output
		if req.Output.JSONSchema != nil {
			raw, _ := json.Marshal(req.Output.JSONSchema)
			_ = json.Unmarshal(raw, &output.JSONSchema)
		}
		out.Output = &output
	}
	return out
}
