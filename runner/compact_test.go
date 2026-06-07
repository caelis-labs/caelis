package runner

import (
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

func TestCompactEvents(t *testing.T) {
	events := []session.Event{
		{Kind: session.EventKindUser},
		{Kind: session.EventKindAssistant},
	}

	result, compaction := CompactEvents(events, "test compaction")
	if len(result) != len(events) {
		t.Error("events should be preserved")
	}
	if compaction.Kind != session.EventKindCompaction {
		t.Error("compaction event should be created")
	}
	if compaction.CompactionPayload == nil {
		t.Error("compaction payload should be set")
	}
}
