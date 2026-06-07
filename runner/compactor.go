package runner

import (
	"context"
	"fmt"
	"strings"

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

// HeuristicCompactor compacts context by keeping system messages, inserting a
// deterministic summary marker, and retaining recent messages that fit.
type HeuristicCompactor struct{}

func NewHeuristicCompactor() *HeuristicCompactor {
	return &HeuristicCompactor{}
}

func (c *HeuristicCompactor) ShouldCompact(msgs []model.Message, budget int) (bool, string) {
	return NeedsCompaction(msgs, CompactionPolicy{MaxContextTokens: budget})
}

func (c *HeuristicCompactor) Compact(_ context.Context, msgs []model.Message, budget int) ([]model.Message, *session.Event, bool) {
	compacted, ok, summary := CompactModelContext(msgs, compactionTargetTokens(budget))
	if !ok {
		return msgs, nil, false
	}
	retained := retainedMessagesAfterSummary(compacted, summary)
	return compacted, &session.Event{
		Kind:       session.EventKindCompaction,
		Visibility: session.VisibilityCanonical,
		CompactionPayload: &session.CompactionPayload{
			Reason:           "context compaction",
			Previous:         len(msgs),
			Remaining:        len(compacted),
			SummaryText:      summary,
			RetainedMessages: retainedMessagesToSession(retained),
		},
	}, true
}

// LLMSummarizingCompactor uses an LLM to summarize older messages during
// compaction, then retains the generated summary plus recent context.
type LLMSummarizingCompactor struct {
	llm model.LLM
}

// LLMCompactor is retained as a compatibility alias for callers that already
// construct the LLM-backed compactor by its earlier name.
type LLMCompactor = LLMSummarizingCompactor

func NewLLMSummarizingCompactor(llm model.LLM) *LLMSummarizingCompactor {
	return &LLMSummarizingCompactor{llm: llm}
}

// NewLLMCompactor creates a compactor that uses the given LLM for summarization.
func NewLLMCompactor(llm model.LLM) *LLMCompactor {
	return NewLLMSummarizingCompactor(llm)
}

func (c *LLMSummarizingCompactor) ShouldCompact(msgs []model.Message, budget int) (bool, string) {
	total := estimateTokens(msgs)
	watermark := int(float64(budget) * 0.80)
	if total > watermark {
		return true, "token estimate exceeds watermark"
	}
	return false, ""
}

func (c *LLMSummarizingCompactor) Compact(ctx context.Context, msgs []model.Message, budget int) ([]model.Message, *session.Event, bool) {
	if len(msgs) <= 2 {
		return msgs, nil, false
	}
	if c == nil || c.llm == nil {
		return msgs, nil, false
	}
	current := estimateTokens(msgs)
	if current <= int(float64(budget)*0.80) {
		return msgs, nil, false
	}

	systemMsgs, dropped, kept := splitMessagesForCompaction(msgs, compactionTargetTokens(budget))
	if len(dropped) == 0 || len(kept) == 0 {
		return msgs, nil, false
	}
	summaryText := strings.TrimSpace(c.summarize(ctx, dropped))
	if summaryText == "" {
		return NewHeuristicCompactor().Compact(ctx, msgs, budget)
	}
	summary := model.Message{Role: model.RoleSystem, Content: []model.Part{{Text: summaryText}}}
	result := make([]model.Message, 0, len(systemMsgs)+1+len(kept))
	result = append(result, systemMsgs...)
	result = append(result, summary)
	result = append(result, kept...)

	compactionEvent := &session.Event{
		Kind:       session.EventKindCompaction,
		Visibility: session.VisibilityCanonical,
		CompactionPayload: &session.CompactionPayload{
			Reason:           "context compaction",
			Previous:         len(msgs),
			Remaining:        len(result),
			SummaryText:      summaryText,
			RetainedMessages: retainedMessagesToSession(kept),
		},
	}

	return result, compactionEvent, true
}

func (c *LLMSummarizingCompactor) summarize(ctx context.Context, dropped []model.Message) string {
	req := model.Request{Messages: []model.Message{
		{
			Role:    model.RoleSystem,
			Content: []model.Part{{Text: "Summarize the prior conversation for faithful continuation. Preserve user goals, decisions, tool results, unresolved tasks, and provider-visible state. Return only the summary."}},
		},
		{
			Role:    model.RoleUser,
			Content: []model.Part{{Text: renderMessagesForSummary(dropped)}},
		},
	}}
	var b strings.Builder
	for evt, err := range c.llm.Generate(ctx, req) {
		if err != nil {
			return ""
		}
		b.WriteString(evt.TextDelta)
	}
	return b.String()
}

func splitMessagesForCompaction(msgs []model.Message, targetTokens int) ([]model.Message, []model.Message, []model.Message) {
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
		if keptTokens+msgTokens > targetTokens && keepFrom < len(otherMsgs) {
			break
		}
		keptTokens += msgTokens
		keepFrom = i
	}
	if keepFrom <= 0 || keepFrom >= len(otherMsgs) {
		return systemMsgs, nil, otherMsgs
	}
	return systemMsgs, otherMsgs[:keepFrom], otherMsgs[keepFrom:]
}

func renderMessagesForSummary(msgs []model.Message) string {
	var b strings.Builder
	for _, msg := range msgs {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%s:", msg.Role)
		for _, part := range msg.Content {
			switch {
			case part.Text != "":
				b.WriteString("\n")
				b.WriteString(part.Text)
			case part.Reasoning != nil && part.Reasoning.Text != "":
				b.WriteString("\n[reasoning] ")
				b.WriteString(part.Reasoning.Text)
			case part.ToolUse != nil:
				fmt.Fprintf(&b, "\n[tool_use] id=%s name=%s args=%s", part.ToolUse.CallID, part.ToolUse.Name, firstNonEmpty(part.ToolUse.ArgJSON, fmt.Sprint(part.ToolUse.Args)))
			case part.ToolResult != nil:
				fmt.Fprintf(&b, "\n[tool_result] id=%s error=%t content=%s", part.ToolResult.CallID, part.ToolResult.IsError, part.ToolResult.Content)
			}
		}
	}
	return b.String()
}

func compactionTargetTokens(budget int) int {
	if budget <= 0 {
		budget = DefaultCompactionPolicy().MaxContextTokens
	}
	return int(float64(budget) * 0.60)
}

func retainedMessagesAfterSummary(messages []model.Message, summary string) []model.Message {
	out := make([]model.Message, 0, len(messages))
	skippedSummary := false
	for _, msg := range messages {
		if !skippedSummary && msg.Role == model.RoleSystem && len(msg.Content) == 1 && msg.Content[0].Text == summary {
			skippedSummary = true
			continue
		}
		out = append(out, msg)
	}
	return out
}

func retainedMessagesToSession(messages []model.Message) []session.CompactionRetainedMessage {
	if len(messages) == 0 {
		return nil
	}
	out := make([]session.CompactionRetainedMessage, 0, len(messages))
	for _, msg := range messages {
		parts := modelPartsToEventParts(msg.Content)
		if len(parts) == 0 {
			continue
		}
		out = append(out, session.CompactionRetainedMessage{
			Role:  string(msg.Role),
			Parts: parts,
		})
	}
	return out
}

func modelPartsToEventParts(parts []model.Part) []session.EventPart {
	out := make([]session.EventPart, 0, len(parts))
	for _, part := range parts {
		switch {
		case part.Text != "":
			out = append(out, session.EventPart{
				Kind:         session.PartKindText,
				Text:         part.Text,
				ProviderMeta: cloneAnyMap(part.ProviderMeta),
			})
		case part.Reasoning != nil:
			out = append(out, session.EventPart{
				Kind:         session.PartKindReasoning,
				Text:         part.Reasoning.Text,
				ProviderMeta: cloneAnyMap(part.ProviderMeta),
			})
		case part.ToolUse != nil:
			out = append(out, session.EventPart{
				Kind: session.PartKindToolUse,
				ToolUse: &session.PartToolUse{
					CallID: part.ToolUse.CallID,
					Name:   part.ToolUse.Name,
					Args:   cloneAnyMap(part.ToolUse.Args),
				},
				ProviderMeta: cloneAnyMap(part.ProviderMeta),
			})
		case part.ToolResult != nil:
			out = append(out, session.EventPart{
				Kind: session.PartKindToolResult,
				ToolResultRef: &session.PartToolResult{
					CallID:  part.ToolResult.CallID,
					Content: part.ToolResult.Content,
					IsError: part.ToolResult.IsError,
				},
				ProviderMeta: cloneAnyMap(part.ProviderMeta),
			})
		}
	}
	return out
}
