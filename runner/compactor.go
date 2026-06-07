package runner

import (
	"context"

	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
)

// Compactor is the contract for context compaction engines.
// Implementations include heuristic truncation and LLM-based summarization.
type Compactor interface {
	// Compact reduces the model context to fit within the token budget.
	// Returns the compacted messages, a durable compaction event, and
	// whether compaction was performed.
	Compact(ctx context.Context, msgs []model.Message, budget int) ([]model.Message, *session.Event, bool)

	// ShouldCompact checks if compaction is needed before a turn.
	ShouldCompact(msgs []model.Message, budget int) (bool, string)
}

// LLMCompactor uses an LLM to summarize older messages during compaction.
type LLMCompactor struct {
	llm model.LLM
}

// NewLLMCompactor creates a compactor that uses the given LLM for summarization.
func NewLLMCompactor(llm model.LLM) *LLMCompactor {
	return &LLMCompactor{llm: llm}
}

func (c *LLMCompactor) ShouldCompact(msgs []model.Message, budget int) (bool, string) {
	total := estimateTokens(msgs)
	watermark := int(float64(budget) * 0.80)
	if total > watermark {
		return true, "token estimate exceeds watermark"
	}
	return false, ""
}

func (c *LLMCompactor) Compact(ctx context.Context, msgs []model.Message, budget int) ([]model.Message, *session.Event, bool) {
	if len(msgs) <= 2 {
		return msgs, nil, false
	}

	current := estimateTokens(msgs)
	if current <= budget {
		return msgs, nil, false
	}

	// Strategy: keep system messages + last N messages that fit.
	var systemMsgs []model.Message
	var otherMsgs []model.Message
	for _, m := range msgs {
		if m.Role == model.RoleSystem {
			systemMsgs = append(systemMsgs, m)
		} else {
			otherMsgs = append(otherMsgs, m)
		}
	}

	keepFrom := len(otherMsgs)
	keptTokens := 0
	for i := len(otherMsgs) - 1; i >= 0; i-- {
		msgTokens := estimateMessageTokens(otherMsgs[i])
		if keptTokens+msgTokens > budget && keepFrom < len(otherMsgs)-1 {
			break
		}
		keptTokens += msgTokens
		keepFrom = i
	}

	if keepFrom == 0 {
		return msgs, nil, false
	}

	summary := model.Message{
		Role: model.RoleSystem,
		Content: []model.Part{
			{Text: "[Context compacted: earlier messages summarized to fit context window]"},
		},
	}

	result := make([]model.Message, 0, len(systemMsgs)+1+len(otherMsgs)-keepFrom)
	result = append(result, systemMsgs...)
	result = append(result, summary)
	result = append(result, otherMsgs[keepFrom:]...)

	compactionEvent := &session.Event{
		Kind:       session.EventKindCompaction,
		Visibility: session.VisibilityCanonical,
		CompactionPayload: &session.CompactionPayload{
			Reason:      "context overflow",
			Previous:    len(msgs),
			Remaining:   len(result),
			SummaryText: summary.Content[0].Text,
		},
	}

	return result, compactionEvent, true
}
