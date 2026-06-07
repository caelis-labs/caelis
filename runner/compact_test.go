package runner

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
)

func TestNeedsCompactionByTokens(t *testing.T) {
	// Create messages that exceed the watermark.
	msgs := make([]model.Message, 10)
	for i := range msgs {
		msgs[i] = model.Message{
			Role:    model.RoleUser,
			Content: []model.Part{{Text: strings.Repeat("x", 10000)}},
		}
	}

	policy := CompactionPolicy{
		WatermarkRatio:   0.80,
		MaxContextTokens: 1000, // very small to trigger
	}

	needs, reason := NeedsCompaction(msgs, policy)
	if !needs {
		t.Error("expected compaction needed")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestNeedsCompactionByCount(t *testing.T) {
	msgs := make([]model.Message, 600)
	for i := range msgs {
		msgs[i] = model.Message{
			Role:    model.RoleUser,
			Content: []model.Part{{Text: "hi"}},
		}
	}

	policy := CompactionPolicy{
		MaxContextTokens:         200000,
		MaxMessagesBeforeCompact: 500,
	}

	needs, _ := NeedsCompaction(msgs, policy)
	if !needs {
		t.Error("expected compaction needed by count")
	}
}

func TestNoCompactionNeeded(t *testing.T) {
	msgs := []model.Message{
		{Role: model.RoleUser, Content: []model.Part{{Text: "hello"}}},
	}

	policy := DefaultCompactionPolicy()
	needs, _ := NeedsCompaction(msgs, policy)
	if needs {
		t.Error("should not need compaction for small context")
	}
}

func TestCompactModelContext(t *testing.T) {
	// Create many messages.
	msgs := make([]model.Message, 100)
	for i := range msgs {
		role := model.RoleUser
		if i%2 == 1 {
			role = model.RoleAssistant
		}
		msgs[i] = model.Message{
			Role:    role,
			Content: []model.Part{{Text: strings.Repeat("x", 1000)}},
		}
	}

	// Compact to a very small target.
	compacted, ok, summary := CompactModelContext(msgs, 5000)
	if !ok {
		t.Error("expected compaction to occur")
	}
	if summary == "" {
		t.Error("expected non-empty summary")
	}
	if len(compacted) >= len(msgs) {
		t.Errorf("compacted %d messages, expected fewer than %d", len(compacted), len(msgs))
	}

	// Should have a summary system message.
	foundSummary := false
	for _, m := range compacted {
		if m.Role == model.RoleSystem && len(m.Content) > 0 {
			if strings.Contains(m.Content[0].Text, "compacted") {
				foundSummary = true
			}
		}
	}
	if !foundSummary {
		t.Error("expected a summary system message")
	}
}

func TestCompactModelContextNoCompaction(t *testing.T) {
	msgs := []model.Message{
		{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}},
		{Role: model.RoleAssistant, Content: []model.Part{{Text: "hello"}}},
	}

	// Large target — no compaction needed.
	compacted, ok, _ := CompactModelContext(msgs, 200000)
	if ok {
		t.Error("should not compact small context")
	}
	if len(compacted) != len(msgs) {
		t.Error("messages should be unchanged")
	}
}

func TestLLMSummarizingCompactorCallsLLMAndPersistsSummary(t *testing.T) {
	msgs := []model.Message{
		{Role: model.RoleSystem, Content: []model.Part{{Text: "system prompt"}}},
		{Role: model.RoleUser, Content: []model.Part{{Text: strings.Repeat("old user ", 1200)}}},
		{Role: model.RoleAssistant, Content: []model.Part{{Text: strings.Repeat("old assistant ", 1200)}}},
		{Role: model.RoleUser, Content: []model.Part{{Text: "latest question"}}},
	}
	llm := &summarizerTestLLM{summary: "summary from llm"}
	compactor := NewLLMSummarizingCompactor(llm)

	compacted, event, ok := compactor.Compact(context.Background(), msgs, 400)
	if !ok {
		t.Fatal("Compact() did not compact")
	}
	if llm.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", llm.calls)
	}
	if len(llm.requests) != 1 || len(llm.requests[0].Messages) == 0 {
		t.Fatalf("summarizer request = %#v, want model-visible messages", llm.requests)
	}
	if got := llm.requests[0].Messages[len(llm.requests[0].Messages)-1].Content[0].Text; !strings.Contains(got, "old user") {
		t.Fatalf("summarizer prompt = %q, want compacted conversation content", got)
	}
	if event == nil || event.Kind != session.EventKindCompaction || event.CompactionPayload == nil {
		t.Fatalf("event = %#v, want compaction payload", event)
	}
	if event.CompactionPayload.SummaryText != "summary from llm" {
		t.Fatalf("summary = %q, want LLM summary", event.CompactionPayload.SummaryText)
	}
	if len(compacted) < 3 {
		t.Fatalf("compacted messages = %#v, want system, summary, and recent message", compacted)
	}
	if compacted[1].Role != model.RoleSystem || compacted[1].Content[0].Text != "summary from llm" {
		t.Fatalf("summary message = %#v, want LLM summary system message", compacted[1])
	}
	if got := compacted[len(compacted)-1].Content[0].Text; got != "latest question" {
		t.Fatalf("last message = %q, want latest question", got)
	}

	rebuilt := session.ModelContextFromEvents([]session.Event{
		{
			Kind:       session.EventKindUser,
			Visibility: session.VisibilityCanonical,
			UserPayload: &session.UserPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "old user content"}},
			},
		},
		*event,
		{
			Kind:       session.EventKindUser,
			Visibility: session.VisibilityCanonical,
			UserPayload: &session.UserPayload{
				Parts: []session.EventPart{{Kind: session.PartKindText, Text: "after compaction"}},
			},
		},
	})
	if len(rebuilt) != 3 {
		t.Fatalf("rebuilt messages = %#v, want summary, retained message, and post-compaction user", rebuilt)
	}
	if rebuilt[0].Role != model.RoleSystem || rebuilt[0].Content[0].Text != "summary from llm" {
		t.Fatalf("rebuilt summary = %#v, want LLM summary", rebuilt[0])
	}
	if rebuilt[1].Content[0].Text != "latest question" {
		t.Fatalf("rebuilt retained message = %#v, want latest question", rebuilt[1])
	}
	if rebuilt[2].Content[0].Text != "after compaction" {
		t.Fatalf("rebuilt post-compaction message = %#v", rebuilt[2])
	}
}

type summarizerTestLLM struct {
	summary  string
	calls    int
	requests []model.Request
}

func (m *summarizerTestLLM) Name() string { return "summarizer-test" }

func (m *summarizerTestLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	m.calls++
	m.requests = append(m.requests, req)
	return func(yield func(model.ResponseEvent, error) bool) {
		yield(model.ResponseEvent{TextDelta: m.summary}, nil)
	}
}
