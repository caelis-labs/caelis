package session

import (
	"encoding/json"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"testing"
)

func TestInvocationAccountingRequiresMatchingDurableReceipt(t *testing.T) {
	receipt := NewModelInvocationReceipt(model.Invocation{ID: "attempt", Provider: "p", Model: "m", Usage: model.Usage{TotalTokens: 12}, Outcome: "completed"}, "chat")
	receipt.SessionID = "s"
	receipt.Scope = &EventScope{TurnID: "turn"}
	response := &Event{SessionID: "s", Type: EventTypeAssistant, Visibility: VisibilityCanonical, Scope: &EventScope{TurnID: "turn"}, Invocation: &EventInvocation{Provider: "p", Model: "m"}, Meta: map[string]any{"caelis": map[string]any{"sdk": map[string]any{"usage_receipt_id": "attempt", "usage": map[string]any{"total_tokens": 12}}}}}
	if got := InvocationAccountingEvents([]*Event{receipt, response}); len(got) != 1 || got[0] != receipt {
		t.Fatalf("matching receipt not selected: %#v", got)
	}
	for _, mutation := range []struct {
		name  string
		apply func(*Event)
	}{
		{"session", func(e *Event) { e.SessionID = "other" }},
		{"turn", func(e *Event) { e.Scope.TurnID = "other" }},
		{"provider", func(e *Event) { e.Invocation.Provider = "other" }},
		{"model", func(e *Event) { e.Invocation.Model = "other" }},
		{"tool_claim", func(e *Event) { e.Type = EventTypeToolResult; e.Visibility = VisibilityCanonical }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			other := CloneEvent(receipt)
			mutation.apply(other)
			if got := InvocationAccountingEvents([]*Event{other, response}); len(got) != 2 {
				t.Fatalf("invalid receipt suppressed usage: %#v", got)
			}
		})
	}
	if got := InvocationAccountingEvents([]*Event{response}); len(got) != 1 {
		t.Fatal("dangling reference dropped legacy measurement")
	}
	if got := InvocationAccountingEvents([]*Event{receipt, CloneEvent(receipt), response}); len(got) != 1 {
		t.Fatal("duplicate receipt counted")
	}
	if IsMainInvocationVisibleEvent(receipt) || IsClientReplayEvent(receipt) {
		t.Fatal("receipt entered context/replay")
	}
}

func TestStableInvocationIdentity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		provider     string
		model        string
		wantProvider string
		wantModel    string
	}{
		{
			name:         "legacy xai response label",
			provider:     " XAI ",
			model:        " grok-4.5-build ",
			wantProvider: "xai",
			wantModel:    "grok-4.5",
		},
		{
			name:         "requested xai model",
			provider:     "xai",
			model:        "grok-4.5",
			wantProvider: "xai",
			wantModel:    "grok-4.5",
		},
		{
			name:         "xai sibling model",
			provider:     "xai",
			model:        "grok-4.20-build",
			wantProvider: "xai",
			wantModel:    "grok-4.20-build",
		},
		{
			name:         "other provider",
			provider:     "custom",
			model:        "grok-4.5-build",
			wantProvider: "custom",
			wantModel:    "grok-4.5-build",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, modelName := StableInvocationIdentity(test.provider, test.model)
			if provider != test.wantProvider || modelName != test.wantModel {
				t.Fatalf(
					"StableInvocationIdentity(%q, %q) = %q/%q, want %q/%q",
					test.provider,
					test.model,
					provider,
					modelName,
					test.wantProvider,
					test.wantModel,
				)
			}
		})
	}
}

func TestCacheUsageRawAndCanonicalPresenceDoNotDoubleCount(t *testing.T) {
	cases := []struct {
		name                       string
		payload                    map[string]any
		prompt, read, write, total int
	}{
		{"raw", map[string]any{"input_tokens": 100, "cache_read_input_tokens": 8000, "cache_creation_input_tokens": 2000, "output_tokens": 200, "cache_creation": map[string]any{"ephemeral_5m_input_tokens": 1500, "ephemeral_1h_input_tokens": 500}}, 2100, 8000, 2000, 10300},
		{"canonical", map[string]any{"prompt_tokens": 2100, "cached_input_tokens": 8000, "cache_write_tokens": 2000, "completion_tokens": 200, "total_tokens": 10300}, 2100, 8000, 2000, 10300},
		{"mixed_zero", map[string]any{"prompt_tokens": 0, "input_tokens": 100, "cached_input_tokens": 0, "cached_input_tokens_reported": true, "cache_read_input_tokens": 8000, "cache_write_tokens": 0, "cache_creation_input_tokens": 2000, "completion_tokens": 0, "output_tokens": 200}, 0, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := usageSnapshotFromPayload(tc.payload, "anthropic")
			if u == nil || u.PromptTokens != tc.prompt || u.CachedInputTokens != tc.read || u.CacheWriteTokens != tc.write || u.TotalTokens != tc.total || !u.CacheWriteTokensReported {
				t.Fatalf("usage=%+v", u)
			}
		})
	}
	zero := usageSnapshotFromPayload(map[string]any{"cache_creation": map[string]any{"ephemeral_5m_input_tokens": 0}}, "anthropic")
	if zero == nil || !zero.CacheWrite5mTokensReported || zero.CacheWriteTokensReported {
		t.Fatalf("TTL-only zero=%+v", zero)
	}
	if usageSnapshotFromPayload(map[string]any{"cache_creation_input_tokens": nil}, "anthropic") != nil {
		t.Fatal("null reported as observed zero")
	}
}

func TestInvocationMetadataCachePresenceSurvivesJSON(t *testing.T) {
	for _, tc := range []struct {
		name        string
		usage       model.Usage
		read, write bool
	}{
		{"missing", model.Usage{PromptTokens: 1, TotalTokens: 1}, false, false},
		{"reported_zero", model.Usage{PromptTokens: 1, TotalTokens: 1, CachedInputTokensReported: true, CacheWriteTokensReported: true}, true, true},
		{"legacy_positive", model.Usage{PromptTokens: 10, TotalTokens: 10, CachedInputTokens: 5, CacheWriteTokens: 2}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(ModelUsageMetadata("anthropic", tc.usage))
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err = json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			payload["cached_tokens"] = 128
			payload["prompt_cache_hit_tokens"] = 128
			usage := UsageSnapshotFromMapForProvider(payload, "anthropic")
			if usage == nil || usage.CachedInputTokens != tc.usage.CachedInputTokens || usage.CachedInputTokensReported != tc.read || usage.CacheWriteTokensReported != tc.write {
				t.Fatalf("usage=%+v metadata=%s", usage, raw)
			}
		})
	}
}

func TestRawResponsesUncachedMeasurementSurvivesLegacyRead(t *testing.T) {
	for _, tc := range []struct {
		details string
		present bool
	}{
		{`{}`, false},
		{`{"cached_tokens":null}`, false},
		{`{"cached_tokens":0}`, true},
	} {
		var payload map[string]any
		if err := json.Unmarshal([]byte(`{"input_tokens":1024,"input_tokens_details":`+tc.details+`}`), &payload); err != nil {
			t.Fatal(err)
		}
		usage := UsageSnapshotFromMapForProvider(payload, "openai")
		if usage == nil || usage.CachedInputTokensReported != tc.present || usage.CachedInputTokens != 0 || usage.PromptTokens != 1024 {
			t.Fatalf("details %s: %+v", tc.details, usage)
		}
	}
}

func TestUsageSnapshotFromMapForProviderAcceptsGeminiUsageMetadata(t *testing.T) {
	usage := UsageSnapshotFromMapForProvider(map[string]any{
		"usageMetadata": map[string]any{
			"promptTokenCount":        11,
			"cachedContentTokenCount": 7,
			"candidatesTokenCount":    2,
			"thoughtsTokenCount":      5,
			"toolUsePromptTokenCount": 3,
			"totalTokenCount":         21,
		},
	}, "")

	if usage == nil {
		t.Fatal("UsageSnapshotFromMapForProvider() = nil, want usage")
		return
	}
	if usage.PromptTokens != 11 {
		t.Fatalf("PromptTokens = %d, want Gemini promptTokenCount", usage.PromptTokens)
	}
	if usage.CachedInputTokens != 7 {
		t.Fatalf("CachedInputTokens = %d, want Gemini cachedContentTokenCount", usage.CachedInputTokens)
	}
	if usage.CompletionTokens != 2 {
		t.Fatalf("CompletionTokens = %d, want Gemini candidatesTokenCount", usage.CompletionTokens)
	}
	if usage.ReasoningTokens != 5 {
		t.Fatalf("ReasoningTokens = %d, want Gemini thoughtsTokenCount", usage.ReasoningTokens)
	}
	if usage.TotalTokens != 21 {
		t.Fatalf("TotalTokens = %d, want Gemini totalTokenCount", usage.TotalTokens)
	}
}

func TestUsageSnapshotFromSessionEventUsesInvocationContextWindow(t *testing.T) {
	usage := UsageSnapshotFromSessionEvent(&Event{
		Invocation: &EventInvocation{Provider: "deepseek", Model: "deepseek-v4-flash", ContextWindowTokens: 128000},
		Meta: map[string]any{
			"usage": map[string]any{
				"prompt_tokens":     11,
				"completion_tokens": 6,
				"total_tokens":      17,
			},
		},
	})

	if usage == nil {
		t.Fatal("UsageSnapshotFromSessionEvent() = nil, want usage")
		return
	}
	if usage.TotalTokens != 17 || usage.ContextWindowTokens != 128000 {
		t.Fatalf("usage = %+v, want total tokens and invocation context window", *usage)
	}
}

func TestUsageSnapshotFromSessionEventUsesTypedContextGauge(t *testing.T) {
	t.Parallel()

	event := &Event{ContextUsage: &ContextUsageSnapshot{
		Size: 200000,
		Used: 42000,
		Meta: map[string]json.RawMessage{"vendor": json.RawMessage(`{"trace":"usage"}`)},
		Cost: &ContextUsageCost{Amount: 0.47, Currency: " USD "},
	}}
	usage := UsageSnapshotFromSessionEvent(event)
	if usage == nil || usage.TotalTokens != 42000 || usage.ContextWindowTokens != 200000 {
		t.Fatalf("UsageSnapshotFromSessionEvent() = %#v, want typed used/size gauge", usage)
	}
	cloned := CloneEvent(event)
	cloned.ContextUsage.Cost.Amount = 1
	cloned.ContextUsage.Meta["vendor"][0] = '['
	if event.ContextUsage.Cost.Amount != 0.47 || cloned.ContextUsage.Cost.Currency != "USD" {
		t.Fatalf("CloneEvent() context usage = original %#v cloned %#v", event.ContextUsage, cloned.ContextUsage)
	}
	if string(event.ContextUsage.Meta["vendor"]) != `{"trace":"usage"}` {
		t.Fatalf("CloneEvent() mutated context usage metadata: %s", event.ContextUsage.Meta["vendor"])
	}
}

func TestUsageSnapshotFromSessionEventUsesSDKContextWindowMetadata(t *testing.T) {
	usage := UsageSnapshotFromSessionEvent(&Event{
		Meta: map[string]any{
			"caelis": map[string]any{
				"sdk": map[string]any{
					"context_window_tokens": 200000,
					"usage": map[string]any{
						"prompt_tokens":     11,
						"completion_tokens": 6,
						"total_tokens":      17,
					},
				},
			},
		},
	})

	if usage == nil {
		t.Fatal("UsageSnapshotFromSessionEvent() = nil, want usage")
		return
	}
	if usage.TotalTokens != 17 || usage.ContextWindowTokens != 200000 {
		t.Fatalf("usage = %+v, want total tokens and sdk context window", *usage)
	}
}

func TestUsageSnapshotFromMapForProviderAcceptsAnthropicCacheReadUsage(t *testing.T) {
	usage := UsageSnapshotFromMapForProvider(map[string]any{
		"input_tokens":                11,
		"cache_creation_input_tokens": 3,
		"cache_read_input_tokens":     7,
		"output_tokens":               2,
	}, "")

	if usage == nil {
		t.Fatal("UsageSnapshotFromMapForProvider() = nil, want usage")
		return
	}
	if usage.PromptTokens != 14 {
		t.Fatalf("PromptTokens = %d, want input plus cache creation", usage.PromptTokens)
	}
	if usage.CachedInputTokens != 7 {
		t.Fatalf("CachedInputTokens = %d, want cache read tokens", usage.CachedInputTokens)
	}
	if usage.CompletionTokens != 2 {
		t.Fatalf("CompletionTokens = %d, want output tokens", usage.CompletionTokens)
	}
	if usage.TotalTokens != 23 {
		t.Fatalf("TotalTokens = %d, want prompt plus cache read plus output", usage.TotalTokens)
	}
}

func TestUsageSnapshotFromMapForProviderDoesNotTrustUndercountedCacheTotal(t *testing.T) {
	usage := UsageSnapshotFromMapForProvider(map[string]any{
		"input_tokens":            10,
		"cache_read_input_tokens": 100,
		"output_tokens":           5,
		"total_tokens":            15,
	}, "deepseek")

	if usage == nil {
		t.Fatal("UsageSnapshotFromMapForProvider() = nil, want usage")
		return
	}
	if usage.TotalTokens != 115 {
		t.Fatalf("TotalTokens = %d, want max(explicit total, prompt+cached+output)", usage.TotalTokens)
	}
}

func TestUsageSnapshotFromMapForProviderAcceptsAnthropicThinkingTokenDetails(t *testing.T) {
	usage := UsageSnapshotFromMapForProvider(map[string]any{
		"input_tokens":  10,
		"output_tokens": 7,
		"output_tokens_details": map[string]any{
			"thinking_tokens": 3,
		},
	}, "")

	if usage == nil {
		t.Fatal("UsageSnapshotFromMapForProvider() = nil, want usage")
		return
	}
	if usage.ReasoningTokens != 3 {
		t.Fatalf("ReasoningTokens = %d, want Anthropic thinking token details", usage.ReasoningTokens)
	}
	if usage.TotalTokens != 17 {
		t.Fatalf("TotalTokens = %d, want input plus output", usage.TotalTokens)
	}
}
