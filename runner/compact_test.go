package runner

import (
	"context"
	"fmt"
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
	if event.SummaryText != "summary from llm" {
		t.Fatalf("summary = %q, want LLM summary", event.SummaryText)
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

func TestIsContextOverflowErrorKeywords(t *testing.T) {
	keywords := []string{
		"context length exceeded",
		"the context window is too large",
		"prompt is too long for the model",
		"too many tokens in the request",
		"maximum context length reached",
		"input is too long for this model",
		"token limit exceeded",
		"max context length exceeded",
		"context overflow detected",
	}
	for _, kw := range keywords {
		if !isContextOverflowError(fmt.Errorf("%s", kw)) {
			t.Errorf("isContextOverflowError(%q) = false, want true", kw)
		}
	}
}

func TestIsContextOverflowErrorTyped(t *testing.T) {
	err := &model.ContextOverflowError{Cause: fmt.Errorf("prompt is too long")}
	if !isContextOverflowError(err) {
		t.Error("isContextOverflowError(ContextOverflowError) = false, want true")
	}
	wrapped := fmt.Errorf("wrapped: %w", err)
	if !isContextOverflowError(wrapped) {
		t.Error("isContextOverflowError(wrapped ContextOverflowError) = false, want true")
	}
}

func TestIsContextOverflowErrorNilAndNonMatching(t *testing.T) {
	if isContextOverflowError(nil) {
		t.Error("isContextOverflowError(nil) = true, want false")
	}
	if isContextOverflowError(fmt.Errorf("some other error")) {
		t.Error("isContextOverflowError(non-matching) = true, want false")
	}
}

func TestEstimateTextTokensCJK(t *testing.T) {
	// CJK characters should be ~1 token each.
	cjk := strings.Repeat("你", 100) // 100 CJK chars
	tokens := estimateTextTokens(cjk)
	if tokens < 90 || tokens > 110 {
		t.Errorf("CJK tokens = %d, want ~100", tokens)
	}

	// ASCII should be ~4 chars per token.
	ascii := strings.Repeat("a", 400) // 400 ASCII chars
	tokens = estimateTextTokens(ascii)
	if tokens < 90 || tokens > 110 {
		t.Errorf("ASCII tokens = %d, want ~100", tokens)
	}

	// Mixed CJK + ASCII.
	mixed := "hello你好world世界" // 5 ASCII + 2 CJK + 5 ASCII + 2 CJK = 14 chars
	tokens = estimateTextTokens(mixed)
	// Expect: 10 ASCII / 4 = 2, 4 CJK = 4, total = 6
	if tokens < 4 || tokens > 8 {
		t.Errorf("mixed tokens = %d, want ~6", tokens)
	}
}

func TestCompactModelContextKeepsLastMessageWhenFirstExceedsBudget(t *testing.T) {
	// One huge message that exceeds the budget.
	msgs := []model.Message{
		{Role: model.RoleUser, Content: []model.Part{{Text: strings.Repeat("x", 100000)}}},
		{Role: model.RoleAssistant, Content: []model.Part{{Text: strings.Repeat("y", 100000)}}},
		{Role: model.RoleUser, Content: []model.Part{{Text: "last"}}},
	}
	// Very small budget — should still keep at least the last message.
	compacted, ok, _ := CompactModelContext(msgs, 10)
	if !ok {
		t.Error("expected compaction to occur even with tiny budget")
	}
	if len(compacted) < 2 {
		t.Errorf("compacted = %d messages, want >= 2 (summary + last)", len(compacted))
	}
}
