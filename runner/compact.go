package runner

import (
	"fmt"

	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
)

// CompactionPolicy defines when and how to compact the model context.
type CompactionPolicy struct {
	// WatermarkRatio triggers proactive compaction when estimated tokens
	// exceed this fraction of MaxContextTokens. Default: 0.80.
	WatermarkRatio float64

	// MaxContextTokens is the model's context window size. Default: 200000.
	MaxContextTokens int

	// MaxMessagesBeforeCompact forces compaction when message count
	// exceeds this threshold. Default: 500.
	MaxMessagesBeforeCompact int
}

// DefaultCompactionPolicy returns sensible defaults.
func DefaultCompactionPolicy() CompactionPolicy {
	return CompactionPolicy{
		WatermarkRatio:           0.80,
		MaxContextTokens:         200000,
		MaxMessagesBeforeCompact: 500,
	}
}

// NeedsCompaction checks if the model context should be compacted.
func NeedsCompaction(msgs []model.Message, policy CompactionPolicy) (bool, string) {
	if policy.MaxContextTokens <= 0 {
		policy.MaxContextTokens = 200000
	}
	if policy.WatermarkRatio <= 0 {
		policy.WatermarkRatio = 0.80
	}

	totalTokens := estimateTokens(msgs)
	watermark := int(float64(policy.MaxContextTokens) * policy.WatermarkRatio)

	if totalTokens > watermark {
		return true, fmt.Sprintf("estimated %d tokens exceeds watermark %d (%.0f%% of %d)",
			totalTokens, watermark, policy.WatermarkRatio*100, policy.MaxContextTokens)
	}

	if policy.MaxMessagesBeforeCompact > 0 && len(msgs) > policy.MaxMessagesBeforeCompact {
		return true, fmt.Sprintf("message count %d exceeds limit %d",
			len(msgs), policy.MaxMessagesBeforeCompact)
	}

	return false, ""
}

// CompactEvents creates a compaction event that summarizes older events.
// The compaction event replaces the summarized events in the model context.
// Returns the new event set with the compaction event prepended.
func CompactEvents(events []session.Event, reason string) ([]session.Event, session.Event) {
	// Create a compaction event that records what was compacted.
	compaction := session.Event{
		Kind:       session.EventKindCompaction,
		Visibility: session.VisibilityCanonical,
		CompactionPayload: &session.CompactionPayload{
			Reason:    reason,
			Previous:  len(events),
			Remaining: 0, // all events are kept, compaction is metadata-only
		},
	}

	// For now, keep all events (metadata-only compaction).
	// Real LLM-based summarization would replace older events here.
	return events, compaction
}

// CompactModelContext performs overflow recovery by summarizing older
// messages and returning a reduced context. Returns the compacted
// messages, whether compaction was performed, and the summary text.
func CompactModelContext(msgs []model.Message, targetTokens int) ([]model.Message, bool, string) {
	if len(msgs) <= 2 {
		return msgs, false, ""
	}

	current := estimateTokens(msgs)
	if current <= targetTokens {
		return msgs, false, ""
	}

	// Strategy: keep system messages, keep the last N messages that fit,
	// and create a summary placeholder for the middle.
	// This is a basic strategy — LLM-based summarization is Phase 5.6+.

	var systemMsgs []model.Message
	var otherMsgs []model.Message
	for _, m := range msgs {
		if m.Role == model.RoleSystem {
			systemMsgs = append(systemMsgs, m)
		} else {
			otherMsgs = append(otherMsgs, m)
		}
	}

	// Keep the last N messages that fit within target.
	keepFrom := len(otherMsgs)
	keptTokens := 0
	for i := len(otherMsgs) - 1; i >= 0; i-- {
		msgTokens := estimateMessageTokens(otherMsgs[i])
		if keptTokens+msgTokens > targetTokens && keepFrom < len(otherMsgs)-1 {
			break
		}
		keptTokens += msgTokens
		keepFrom = i
	}

	if keepFrom == 0 {
		return msgs, false, ""
	}

	// Create summary message for the dropped portion.
	droppedCount := keepFrom
	summary := model.Message{
		Role: model.RoleSystem,
		Content: []model.Part{
			{Text: fmt.Sprintf("[Context compacted: %d earlier messages summarized to fit context window]", droppedCount)},
		},
	}

	// Build compacted context: system + summary + kept messages.
	result := make([]model.Message, 0, len(systemMsgs)+1+len(otherMsgs)-keepFrom)
	result = append(result, systemMsgs...)
	result = append(result, summary)
	result = append(result, otherMsgs[keepFrom:]...)

	return result, true, summary.Content[0].Text
}

// estimateTokens estimates the total token count of a message sequence.
func estimateTokens(msgs []model.Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateMessageTokens(m)
	}
	return total
}

// estimateMessageTokens estimates the token count of a single message.
func estimateMessageTokens(m model.Message) int {
	total := 4 // role overhead
	for _, p := range m.Content {
		total += len(p.Text) / 4
		if p.ToolUse != nil {
			total += len(p.ToolUse.Name) / 4
			total += 10 // args overhead
		}
		if p.ToolResult != nil {
			total += len(p.ToolResult.Content) / 4
		}
	}
	return total
}
