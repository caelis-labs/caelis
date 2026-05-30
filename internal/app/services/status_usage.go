package services

import (
	"encoding/json"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

const (
	usageCategoryMain       = "main"
	usageCategorySubagent   = "subagent"
	usageCategoryAutoReview = "auto_review"
	usageCategoryCompaction = "compact"
)

func statusUsageView(snapshot session.Snapshot) appviewmodel.UsageStatus {
	var out appviewmodel.UsageStatus
	for _, event := range snapshot.Events {
		if session.IsTransient(event) {
			continue
		}
		usage, ok := usageFromSessionEvent(event)
		if !ok {
			continue
		}
		addStatusUsage(&out, usageCategoryFromCoreEvent(event), usage)
	}
	return out
}

func addStatusUsage(status *appviewmodel.UsageStatus, category string, usage appviewmodel.TokenUsage) {
	if status == nil || tokenUsageEmpty(usage) {
		return
	}
	addTokenUsage(&status.Total, usage)
	switch category {
	case usageCategorySubagent:
		addTokenUsage(&status.Subagents, usage)
	case usageCategoryAutoReview:
		addTokenUsage(&status.AutoReview, usage)
	case usageCategoryCompaction:
		addTokenUsage(&status.Compaction, usage)
	default:
		addTokenUsage(&status.Main, usage)
	}
}

func addTokenUsage(total *appviewmodel.TokenUsage, usage appviewmodel.TokenUsage) {
	if total == nil {
		return
	}
	total.InputTokens += usage.InputTokens
	total.CachedInputTokens += usage.CachedInputTokens
	total.OutputTokens += usage.OutputTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.TotalTokens += usage.TotalTokens
	if usage.ContextWindowTokens > total.ContextWindowTokens {
		total.ContextWindowTokens = usage.ContextWindowTokens
	}
}

func usageFromSessionEvent(event session.Event) (appviewmodel.TokenUsage, bool) {
	if event.Message != nil && event.Message.Usage != nil {
		return tokenUsageFromModel(*event.Message.Usage)
	}
	if usage, ok := tokenUsageFromAny(event.Meta["usage"]); ok {
		return usage, true
	}
	if compactMeta, ok := mapAny(event.Meta[compactMetaKey]); ok {
		if usage, ok := tokenUsageFromAny(compactMeta["usage"]); ok {
			return usage, true
		}
	}
	if usage, ok := tokenUsageFromAny(nestedAny(event.Meta, "caelis", "sdk", "usage")); ok {
		return usage, true
	}
	return tokenUsageFromAny(event.Meta)
}

func tokenUsageFromModel(usage model.Usage) (appviewmodel.TokenUsage, bool) {
	out := appviewmodel.TokenUsage{
		InputTokens:         usage.InputTokens,
		CachedInputTokens:   usage.CachedInputTokens,
		OutputTokens:        usage.OutputTokens,
		ReasoningTokens:     usage.ReasoningTokens,
		TotalTokens:         usage.TotalTokens,
		ContextWindowTokens: usage.ContextWindowTokens,
	}
	if out.TotalTokens == 0 && (out.InputTokens != 0 || out.OutputTokens != 0) {
		out.TotalTokens = out.InputTokens + out.OutputTokens
	}
	return out, !tokenUsageEmpty(out)
}

func tokenUsageFromAny(value any) (appviewmodel.TokenUsage, bool) {
	payload, ok := mapAny(value)
	if !ok {
		return appviewmodel.TokenUsage{}, false
	}
	inputTokens := firstNonZeroInt(anyInt(payload["input_tokens"]), anyInt(payload["prompt_tokens"]))
	outputTokens := firstNonZeroInt(anyInt(payload["output_tokens"]), anyInt(payload["completion_tokens"]))
	totalTokens := anyInt(payload["total_tokens"])
	if totalTokens == 0 && (inputTokens != 0 || outputTokens != 0) {
		totalTokens = inputTokens + outputTokens
	}
	out := appviewmodel.TokenUsage{
		InputTokens:         inputTokens,
		CachedInputTokens:   firstNonZeroInt(anyInt(payload["cached_input_tokens"]), anyInt(payload["cached_prompt_tokens"]), anyInt(payload["cache_read_input_tokens"])),
		OutputTokens:        outputTokens,
		ReasoningTokens:     firstNonZeroInt(anyInt(payload["reasoning_tokens"]), anyInt(payload["reasoning_output_tokens"])),
		TotalTokens:         totalTokens,
		ContextWindowTokens: anyInt(payload["context_window_tokens"]),
	}
	return out, !tokenUsageEmpty(out)
}

func usageCategoryFromCoreEvent(event session.Event) string {
	if category := normalizeUsageCategory(eventMetaString(event.Meta, "usage_category")); category != "" {
		return category
	}
	if category := normalizeUsageCategory(eventMetaString(event.Meta, "usageCategory")); category != "" {
		return category
	}
	if category := normalizeUsageCategory(eventMetaString(event.Meta, "category")); category != "" {
		return category
	}
	if category := normalizeUsageCategory(stringFromAny(nestedAny(event.Meta, "caelis", "usage", "category"))); category != "" {
		return category
	}
	if category := normalizeUsageCategory(stringFromAny(nestedAny(event.Meta, "caelis", "sdk", "usage_category"))); category != "" {
		return category
	}
	if event.Type == session.EventCompact {
		return usageCategoryCompaction
	}
	if event.Scope != nil && event.Scope.Participant.Kind == session.ParticipantSubagent {
		return usageCategorySubagent
	}
	return usageCategoryMain
}

func normalizeUsageCategory(category string) string {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(category, "-", "_"))) {
	case "auto_review", "autoreview", "review":
		return usageCategoryAutoReview
	case "subagent", "sub_agent", "child", "child_agent":
		return usageCategorySubagent
	case "compact", "compaction":
		return usageCategoryCompaction
	case "main", "controller":
		return usageCategoryMain
	default:
		return ""
	}
}

func tokenUsageEmpty(usage appviewmodel.TokenUsage) bool {
	return usage.InputTokens == 0 &&
		usage.CachedInputTokens == 0 &&
		usage.OutputTokens == 0 &&
		usage.ReasoningTokens == 0 &&
		usage.TotalTokens == 0 &&
		usage.ContextWindowTokens == 0
}

func mapAny(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out, true
	default:
		raw, err := json.Marshal(value)
		if err != nil || len(raw) == 0 || string(raw) == "null" {
			return nil, false
		}
		out := map[string]any{}
		if err := json.Unmarshal(raw, &out); err != nil || len(out) == 0 {
			return nil, false
		}
		return out, true
	}
}

func nestedAny(values map[string]any, path ...string) any {
	if len(values) == 0 {
		return nil
	}
	var current any = values
	for _, key := range path {
		mapped, ok := mapAny(current)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	return current
}

func anyInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	default:
		return 0
	}
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func eventMetaString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	return stringFromAny(meta[key])
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
