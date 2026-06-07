package runner

import (
	"context"
	"fmt"
	"strings"

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
	// Always keep at least the last message to ensure forward progress.
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

	// If all non-system messages fit or we only have 1, nothing to compact.
	if keepFrom == 0 && len(otherMsgs) <= 1 {
		return msgs, false, ""
	}
	// Ensure we always keep at least the last message.
	if keepFrom >= len(otherMsgs) {
		keepFrom = len(otherMsgs) - 1
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
// Uses a rune-aware heuristic: CJK characters typically consume ~1 token each,
// while ASCII/Latin characters average ~4 chars per token.
func estimateMessageTokens(m model.Message) int {
	total := 4 // role overhead
	for _, p := range m.Content {
		total += estimateTextTokens(p.Text)
		if p.ToolUse != nil {
			total += estimateTextTokens(p.ToolUse.Name)
			total += 10 // args overhead
		}
		if p.ToolResult != nil {
			total += estimateTextTokens(p.ToolResult.Content)
		}
	}
	return total
}

// estimateTextTokens estimates tokens for a text string using rune-aware
// counting. CJK characters (Unicode ranges U+4E00-U+9FFF, U+3400-U+4DBF,
// U+F900-U+FAFF) count as ~1 token each; other characters average ~4 per token.
func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	tokens := 0
	asciiCount := 0
	for _, r := range text {
		if isCJK(r) {
			// Flush pending ASCII count.
			tokens += asciiCount / 4
			asciiCount = 0
			tokens++ // CJK ≈ 1 token per character
		} else {
			asciiCount++
		}
	}
	tokens += asciiCount / 4
	return tokens
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0xF900 && r <= 0xFAFF) || // CJK Compatibility Ideographs
		(r >= 0x2E80 && r <= 0x2EFF) || // CJK Radicals
		(r >= 0x3000 && r <= 0x303F) || // CJK Symbols and Punctuation
		(r >= 0xFF00 && r <= 0xFFEF) // Fullwidth Forms
}

func (r *Runner) compactForOverflowRetry(ctx context.Context, ref session.Ref, msgs []model.Message) ([]model.Message, bool, error) {
	budget := DefaultCompactionPolicy().MaxContextTokens
	var (
		compacted []model.Message
		event     *session.Event
		ok        bool
	)
	if r.cfg.Compactor != nil {
		compacted, event, ok = r.cfg.Compactor.Compact(ctx, msgs, budget)
	} else {
		summaryTarget := int(float64(budget) * 0.6)
		var summary string
		compacted, ok, summary = CompactModelContext(msgs, summaryTarget)
		if ok {
			event = &session.Event{
				Kind:       session.EventKindCompaction,
				Visibility: session.VisibilityCanonical,
				CompactionPayload: &session.CompactionPayload{
					Reason:           "context overflow retry",
					Previous:         len(msgs),
					Remaining:        len(compacted),
					SummaryText:      summary,
					RetainedMessages: retainedMessagesToSession(retainedMessagesAfterSummary(compacted, summary)),
				},
			}
		}
	}
	if !ok {
		return msgs, false, nil
	}
	if event != nil {
		if event.CompactionPayload != nil {
			event.CompactionPayload.Reason = "context overflow retry"
		}
		if _, err := r.cfg.Sessions.AppendEvent(ctx, ref, *event); err != nil {
			return nil, false, fmt.Errorf("runner: persist overflow compaction event: %w", err)
		}
	}
	return compacted, true, nil
}

func (r *Runner) compactBeforeInvocation(ctx context.Context, ref session.Ref, msgs []model.Message) ([]model.Message, error) {
	if r.cfg.Compactor != nil {
		return r.compactBeforeInvocationWithConfiguredCompactor(ctx, ref, msgs)
	}
	return r.compactBeforeInvocationWithHeuristic(ctx, ref, msgs)
}

func (r *Runner) compactBeforeInvocationWithConfiguredCompactor(ctx context.Context, ref session.Ref, msgs []model.Message) ([]model.Message, error) {
	budget := DefaultCompactionPolicy().MaxContextTokens
	if ok, _ := r.cfg.Compactor.ShouldCompact(msgs, budget); !ok {
		return msgs, nil
	}
	compactedMsgs, compactionEvt, didCompact := r.cfg.Compactor.Compact(ctx, msgs, budget)
	if !didCompact {
		return msgs, nil
	}
	if compactionEvt != nil {
		if _, err := r.cfg.Sessions.AppendEvent(ctx, ref, *compactionEvt); err != nil {
			return nil, fmt.Errorf("runner: persist compaction event: %w", err)
		}
	}
	return compactedMsgs, nil
}

func (r *Runner) compactBeforeInvocationWithHeuristic(ctx context.Context, ref session.Ref, msgs []model.Message) ([]model.Message, error) {
	policy := DefaultCompactionPolicy()
	needsCompaction, reason := NeedsCompaction(msgs, policy)
	if !needsCompaction {
		return msgs, nil
	}
	target := int(float64(policy.MaxContextTokens) * 0.6)
	compactedMsgs, ok, summaryText := CompactModelContext(msgs, target)
	if !ok {
		return msgs, nil
	}
	if _, err := r.cfg.Sessions.AppendEvent(ctx, ref, session.Event{
		Kind:       session.EventKindCompaction,
		Visibility: session.VisibilityCanonical,
		CompactionPayload: &session.CompactionPayload{
			Reason:           reason,
			Previous:         len(msgs),
			Remaining:        len(compactedMsgs),
			SummaryText:      summaryText,
			RetainedMessages: retainedMessagesToSession(retainedMessagesAfterSummary(compactedMsgs, summaryText)),
		},
	}); err != nil {
		return nil, fmt.Errorf("runner: persist compaction event: %w", err)
	}
	return compactedMsgs, nil
}

func isContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	if model.IsContextOverflow(err) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, keyword := range []string{
		"context length",
		"context window",
		"prompt is too long",
		"too many tokens",
		"maximum context",
		"input is too long",
		"token limit",
		"max context",
		"context overflow",
	} {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}
