package local

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func snapshotUsageWithResolvedWindow(promptEvents []*session.Event, window int, cfg CompactionConfig) compact.UsageSnapshot {
	cfg = normalizeCompactionConfig(cfg)
	if window <= 0 {
		window = cfg.DefaultContextWindowTokens
	}
	reserve := resolveReserveOutputTokens(window, cfg.ReserveOutputTokens)
	safety := resolveSafetyMarginTokens(window, cfg.SafetyMarginTokens)
	effective := resolveEffectiveInputBudget(window, reserve, safety)

	total := 0
	delta := 0
	prefix := 0
	asOfEventID := ""
	source := compact.UsageSourceEstimated
	if snapshot, ok := latestProviderTokenSnapshot(promptEvents); ok {
		total = snapshot.BaselineTokens
		delta = estimateTokensFromIndex(promptEvents, snapshot.DeltaStartIndex)
		total += delta
		asOfEventID = snapshot.EventID
		source = compact.UsageSourceProvider
	} else {
		prefix = cfg.EstimatedPromptPrefixTokens
		total = estimatePromptEventsTokens(promptEvents) + prefix
	}
	return compact.UsageSnapshot{
		TotalTokens:           total,
		ContextWindowTokens:   window,
		EffectiveInputBudget:  effective,
		EstimatedDeltaTokens:  delta,
		EstimatedPrefixTokens: prefix,
		Source:                source,
		AsOfEventID:           asOfEventID,
	}
}

func splitEventsByTokenBudget(events []*session.Event, budget int) [][]*session.Event {
	if budget <= 0 {
		budget = 24000
	}
	chunks := make([][]*session.Event, 0, 4)
	current := make([]*session.Event, 0, 8)
	currentTokens := 0
	for _, ev := range events {
		if ev == nil {
			continue
		}
		tokens := estimatePromptEventTokens(ev)
		if len(current) > 0 && currentTokens+tokens > budget {
			chunks = append(chunks, current)
			current = make([]*session.Event, 0, 8)
			currentTokens = 0
		}
		current = append(current, session.CloneEvent(ev))
		currentTokens += tokens
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

type providerTokenSnapshot struct {
	BaselineTokens  int
	DeltaStartIndex int
	EventID         string
}

func latestProviderTokenSnapshot(events []*session.Event) (providerTokenSnapshot, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event == nil || event.Meta == nil {
			continue
		}
		baseline, includeSnapshotGroup, ok := providerPromptBaselineTokens(event.Meta)
		if !ok || baseline <= 0 {
			continue
		}
		start := providerSnapshotGroupStart(events, i)
		deltaStart := start
		if !includeSnapshotGroup {
			deltaStart = i + 1
		}
		if id := strings.TrimSpace(events[start].ID); id != "" {
			return providerTokenSnapshot{
				BaselineTokens:  baseline,
				DeltaStartIndex: deltaStart,
				EventID:         id,
			}, true
		}
		if id := strings.TrimSpace(event.ID); id != "" {
			return providerTokenSnapshot{
				BaselineTokens:  baseline,
				DeltaStartIndex: deltaStart,
				EventID:         id,
			}, true
		}
	}
	return providerTokenSnapshot{}, false
}

func providerPromptBaselineTokens(meta map[string]any) (int, bool, bool) {
	if len(meta) == 0 {
		return 0, false, false
	}
	if usage := nestedMap(meta, "caelis", "sdk", "usage"); len(usage) > 0 {
		if value, ok := intFromAny(usage["prompt_tokens"]); ok && value > 0 {
			return value, true, true
		}
		total, totalOK := intFromAny(usage["total_tokens"])
		completion, completionOK := intFromAny(usage["completion_tokens"])
		if totalOK && completionOK && total > 0 {
			return max(total-completion, 0), true, true
		}
		if totalOK && total > 0 {
			return total, false, true
		}
	}
	if value, ok := intFromAny(meta["prompt_tokens"]); ok && value > 0 {
		return value, true, true
	}
	total, totalOK := intFromAny(meta["total_tokens"])
	completion, completionOK := intFromAny(meta["completion_tokens"])
	if totalOK && completionOK && total > 0 {
		return max(total-completion, 0), true, true
	}
	if totalOK && total > 0 {
		return total, false, true
	}
	return 0, false, false
}

func providerSnapshotGroupStart(events []*session.Event, end int) int {
	if end < 0 || end >= len(events) {
		return end
	}
	target := providerSnapshotSignature(events[end])
	if target == "" {
		return end
	}
	start := end
	for start > 0 {
		prev := events[start-1]
		if prev == nil || providerSnapshotSignature(prev) != target {
			break
		}
		start--
	}
	return start
}

func providerSnapshotSignature(event *session.Event) string {
	if event == nil || len(event.Meta) == 0 {
		return ""
	}
	prompt, _ := intFromAny(event.Meta["prompt_tokens"])
	completion, _ := intFromAny(event.Meta["completion_tokens"])
	total, _ := intFromAny(event.Meta["total_tokens"])
	provider := strings.TrimSpace(stringifyAny(event.Meta["provider"]))
	model := strings.TrimSpace(stringifyAny(event.Meta["model"]))
	if sdkMeta := nestedMap(event.Meta, "caelis", "sdk"); len(sdkMeta) > 0 {
		provider = firstNonEmpty(provider, strings.TrimSpace(stringifyAny(sdkMeta["provider"])))
		model = firstNonEmpty(model, strings.TrimSpace(stringifyAny(sdkMeta["model"])))
		if usage := nestedMap(event.Meta, "caelis", "sdk", "usage"); len(usage) > 0 {
			if value, ok := intFromAny(usage["prompt_tokens"]); ok {
				prompt = value
			}
			if value, ok := intFromAny(usage["completion_tokens"]); ok {
				completion = value
			}
			if value, ok := intFromAny(usage["total_tokens"]); ok {
				total = value
			}
		}
	}
	if prompt <= 0 && completion <= 0 && total <= 0 && provider == "" && model == "" {
		return ""
	}
	return fmt.Sprintf("%s|%s|%d|%d|%d", provider, model, prompt, completion, total)
}

func nestedMap(values map[string]any, path ...string) map[string]any {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	out, _ := current.(map[string]any)
	return out
}

func estimateTokensFromIndex(events []*session.Event, index int) int {
	if index <= 0 {
		return estimatePromptEventsTokens(events)
	}
	total := 0
	for _, event := range events[index:] {
		total += estimatePromptEventTokens(event)
	}
	return total
}

func estimatePromptEventsTokens(events []*session.Event) int {
	total := 0
	for _, event := range events {
		total += estimatePromptEventTokens(event)
	}
	return total
}

func estimatePromptEventTokens(event *session.Event) int {
	if event == nil {
		return 0
	}
	if event.Message != nil {
		return estimateMessageTokens(*event.Message)
	}
	if text := strings.TrimSpace(session.EventText(event)); text != "" {
		return estimateTextTokens(text)
	}
	return 0
}

func estimateMessageTokens(message model.Message) int {
	total := 0
	if text := strings.TrimSpace(message.TextContent()); text != "" {
		total += estimateTextTokens(text)
	}
	for _, call := range message.ToolCalls() {
		total += estimateTextTokens(call.Name) + estimateTextTokens(call.Args)
	}
	for _, result := range message.ToolResults() {
		total += estimateTextTokens(result.Name)
		response := model.Message{
			Role: model.RoleTool,
			Parts: []model.Part{{
				Kind:       model.PartKindToolResult,
				ToolResult: &result,
			}},
		}.ToolResponse()
		if response != nil {
			payload := stringifyAny(response.Result)
			estimated := estimateTextTokens(payload)
			total += max(estimated+32, int(float64(estimated)*1.25))
		}
	}
	return max(total, 1)
}

func resolveContextWindowTokens(llm model.LLM, fallback int) int {
	if provider, ok := llm.(compact.ContextWindowProvider); ok {
		if tokens := provider.ContextWindowTokens(); tokens > 0 {
			return tokens
		}
	}
	return fallback
}

func resolveReserveOutputTokens(window int, configured int) int {
	if configured <= 0 {
		configured = 5000
	}
	if window <= 0 {
		return configured
	}
	maxReserve := max(window/4, 256)
	if configured > maxReserve {
		return maxReserve
	}
	return configured
}

func resolveSafetyMarginTokens(window int, configured int) int {
	if configured <= 0 {
		configured = 2048
	}
	if window <= 0 {
		return configured
	}
	maxSafety := max(window/8, 256)
	if configured > maxSafety {
		return maxSafety
	}
	return configured
}

func resolveEffectiveInputBudget(window, reserve, safety int) int {
	if window <= 0 {
		return 1
	}
	effective := window - reserve - safety
	if effective <= 0 {
		effective = window - reserve
	}
	if effective <= 0 {
		effective = window / 2
	}
	return max(min(effective, window), 1)
}

func dynamicWatermarks(window int, configuredSoft, configuredForce float64) (float64, float64) {
	if configuredSoft > 0 && configuredForce > 0 {
		if configuredForce < configuredSoft {
			configuredForce = configuredSoft
		}
		return configuredSoft, configuredForce
	}
	switch {
	case window >= 200000:
		return 0.95, 0.985
	case window >= 128000:
		return 0.93, 0.975
	case window >= 64000:
		return 0.90, 0.96
	case window >= 32000:
		return 0.85, 0.93
	default:
		return 0.78, 0.88
	}
}

func lastEventID(events []*session.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if id := strings.TrimSpace(events[i].ID); id != "" {
			return id
		}
	}
	return ""
}

func compactText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 12 {
		return text[:limit]
	}
	head := limit / 2
	tail := limit - head - 3
	if tail < 0 {
		tail = 0
	}
	return strings.TrimSpace(text[:head]) + "..." + strings.TrimSpace(text[len(text)-tail:])
}

func stringifyAny(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		raw, _ := json.Marshal(value)
		return string(raw)
	}
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	tokens := len([]rune(text)) / 4
	if len([]rune(text))%4 != 0 {
		tokens++
	}
	return max(tokens, 1)
}

func intFromAny(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		v, err := typed.Int64()
		if err == nil {
			return int(v), true
		}
	}
	return 0, false
}

func isCompactionOverflowError(err error) bool {
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
