package session

import (
	"reflect"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// StableInvocationIdentity returns the provider and requested-model identity
// used to attribute durable invocation usage. Provider response implementation
// labels remain available in raw response metadata.
func StableInvocationIdentity(provider, modelName string) (string, string) {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	if strings.EqualFold(provider, "xai") && strings.EqualFold(modelName, "grok-4.5-build") {
		// Sessions written before requested-model identity became authoritative
		// persisted xAI's response label. Keep this mapping until a Session
		// schema migration rewrites those histories.
		return "xai", "grok-4.5"
	}
	return provider, modelName
}

// NewModelInvocationReceipt builds a Runtime-owned accounting journal event.
// It is durable evidence, excluded from model context and client replay. The
// caller supplies the trusted Session/Scope before appending it under its fence.
func NewModelInvocationReceipt(in model.Invocation, kind string) *Event {
	sdk := map[string]any{"invocation_kind": kind, "outcome": in.Outcome, "usage_status": "unknown"}
	if in.Usage.IsReported() {
		sdk["usage_status"] = "reported"
		sdk["usage"] = ModelUsageMetadata(in.Provider, in.Usage)
	}
	return &Event{
		ID:             in.ID,
		IdempotencyKey: "model-invocation:" + in.ID,
		Type:           EventTypeLifecycle,
		Visibility:     VisibilityJournal,
		Actor:          ActorRef{Kind: ActorKindSystem, Name: "model-runtime"},
		Invocation:     &EventInvocation{Provider: in.Provider, Model: in.Model},
		Lifecycle:      &EventLifecycle{Status: "model_invocation"},
		Meta:           map[string]any{"caelis": map[string]any{"version": 1, "sdk": sdk}},
	}
}

// ModelUsageMetadata preserves normalized provider usage without inventing a
// monetary estimate. A zero CostMicros does not establish price availability.
func ModelUsageMetadata(provider string, usage model.Usage) map[string]any {
	if strings.EqualFold(provider, "deepseek") && usage.CachedInputTokens > 0 && usage.TotalTokens >= usage.PromptTokens+usage.CachedInputTokens+usage.CompletionTokens && usage.PromptTokens+usage.CompletionTokens > 0 {
		provider = "deepseek-anthropic"
	}
	out := map[string]any{
		"provider":            provider,
		"prompt_tokens":       usage.PromptTokens,
		"cached_input_tokens": usage.CachedInputTokens,
		"completion_tokens":   usage.CompletionTokens,
		"reasoning_tokens":    usage.ReasoningTokens,
		"total_tokens":        usage.TotalTokens,
	}
	if usage.CostMicros != 0 {
		out["cost_micros"] = usage.CostMicros
	}
	return out
}

// IsModelInvocationReceipt accepts only the typed Runtime journal shape, not
// a marker embedded in a tool output, response text, or arbitrary metadata.
func IsModelInvocationReceipt(event *Event) bool {
	return event != nil && IsJournal(event) && EventTypeOf(event) == EventTypeLifecycle &&
		event.Lifecycle != nil && event.Lifecycle.Status == "model_invocation" &&
		event.Actor.Kind == ActorKindSystem && event.Actor.Name == "model-runtime" &&
		event.Invocation != nil && event.ID != "" && event.IdempotencyKey == "model-invocation:"+event.ID
}

// InvocationAccountingEvents selects one accounting fact per invocation while
// retaining historical events without receipts. Remove that compatibility read
// only after the supported Session upgrade floor requires invocation receipts.
// A dangling or mismatched reference never suppresses a historical measurement.
func InvocationAccountingEvents(events []*Event) []*Event {
	receipts := map[string]*Event{}
	for _, event := range events {
		if IsModelInvocationReceipt(event) {
			receipts[event.SessionID+"\x00"+event.ID] = event
		}
	}
	seen := map[string]bool{}
	out := make([]*Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if IsModelInvocationReceipt(event) {
			key := event.SessionID + "\x00" + event.ID
			if seen[key] {
				continue
			}
			seen[key] = true
		} else if EventTypeOf(event) == EventTypeAssistant || EventTypeOf(event) == EventTypeToolCall {
			ref, _ := nestedAny(event.Meta, "caelis", "sdk", "usage_receipt_id").(string)
			receipt := receipts[event.SessionID+"\x00"+ref]
			if receipt != nil && event.Invocation != nil && receipt.Invocation.Provider == event.Invocation.Provider && receipt.Invocation.Model == event.Invocation.Model && reflect.DeepEqual(receipt.Scope, event.Scope) {
				continue
			}
		}
		out = append(out, event)
	}
	return out
}
