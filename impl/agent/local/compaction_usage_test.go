package local

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestComputeUsageSnapshotIncludesEstimatedPromptPrefix(t *testing.T) {
	msg := model.NewTextMessage(model.RoleUser, "hello")
	events := []*session.Event{{
		ID:         "u1",
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Message:    &msg,
		Text:       msg.TextContent(),
	}}

	got := ComputeUsageSnapshot(events, nil, 1000, CompactionConfig{
		EstimatedPromptPrefixTokens: 400,
	})

	if got.Source != compact.UsageSourceEstimated {
		t.Fatalf("usage source = %q, want estimated", got.Source)
	}
	if got.EstimatedPrefixTokens != 400 {
		t.Fatalf("estimated prefix = %d, want 400", got.EstimatedPrefixTokens)
	}
	if got.TotalTokens <= 400 {
		t.Fatalf("total tokens = %d, want prompt text plus estimated prefix", got.TotalTokens)
	}
}

func TestComputeUsageSnapshotDoesNotDoubleCountPrefixWithProviderBaseline(t *testing.T) {
	user := model.NewTextMessage(model.RoleUser, "hello")
	assistant := model.NewTextMessage(model.RoleAssistant, "world")
	events := []*session.Event{
		{
			ID:         "u1",
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Message:    &user,
			Text:       user.TextContent(),
		},
		{
			ID:         "a1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &assistant,
			Text:       assistant.TextContent(),
			Meta: map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 5,
				"total_tokens":      105,
			},
		},
	}

	got := ComputeUsageSnapshot(events, nil, 1000, CompactionConfig{
		EstimatedPromptPrefixTokens: 400,
	})

	if got.Source != compact.UsageSourceProvider {
		t.Fatalf("usage source = %q, want provider", got.Source)
	}
	if got.EstimatedPrefixTokens != 0 {
		t.Fatalf("estimated prefix = %d, want 0 when provider baseline exists", got.EstimatedPrefixTokens)
	}
	if got.TotalTokens >= 400 {
		t.Fatalf("total tokens = %d, provider baseline should already include prompt prefix", got.TotalTokens)
	}
}

func TestComputeUsageSnapshotIncludesAnthropicCachedInputBaseline(t *testing.T) {
	user := model.NewTextMessage(model.RoleUser, "hello")
	assistant := model.NewTextMessage(model.RoleAssistant, "answer")
	events := []*session.Event{
		{
			ID:         "u1",
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Message:    &user,
			Text:       user.TextContent(),
		},
		{
			ID:         "a1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &assistant,
			Text:       assistant.TextContent(),
			Meta: map[string]any{
				"caelis": map[string]any{
					"sdk": map[string]any{
						"provider": "deepseek",
						"model":    "deepseek-v4-flash",
						"usage": map[string]any{
							"provider":            "deepseek-anthropic",
							"prompt_tokens":       94,
							"cached_input_tokens": 11008,
							"completion_tokens":   194,
							"total_tokens":        288,
						},
					},
				},
			},
		},
	}

	got := ComputeUsageSnapshot(events, nil, 1048576, CompactionConfig{})

	if got.Source != compact.UsageSourceProvider {
		t.Fatalf("usage source = %q, want provider", got.Source)
	}
	if got.TotalTokens < 11102 {
		t.Fatalf("total tokens = %d, want provider baseline to include cached input", got.TotalTokens)
	}
}

func TestDynamicCompactionDefaultsByContextWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		window        int
		wantReserve   int
		wantSafety    int
		wantEffective int
		wantSoft      float64
		wantForce     float64
		wantEmergency float64
	}{
		{
			name:          "1m",
			window:        1_000_000,
			wantReserve:   62500,
			wantSafety:    16000,
			wantEffective: 921500,
			wantSoft:      0.90,
			wantForce:     0.96,
			wantEmergency: 0.98,
		},
		{
			name:          "200k",
			window:        200000,
			wantReserve:   16666,
			wantSafety:    8000,
			wantEffective: 175334,
			wantSoft:      0.86,
			wantForce:     0.93,
			wantEmergency: 0.96,
		},
		{
			name:          "128k",
			window:        128000,
			wantReserve:   12000,
			wantSafety:    4096,
			wantEffective: 111904,
			wantSoft:      0.82,
			wantForce:     0.90,
			wantEmergency: 0.94,
		},
		{
			name:          "64k",
			window:        64000,
			wantReserve:   6000,
			wantSafety:    2048,
			wantEffective: 55952,
			wantSoft:      0.76,
			wantForce:     0.86,
			wantEmergency: 0.92,
		},
		{
			name:          "32k",
			window:        32000,
			wantReserve:   4000,
			wantSafety:    1536,
			wantEffective: 26464,
			wantSoft:      0.70,
			wantForce:     0.80,
			wantEmergency: 0.88,
		},
		{
			name:          "small",
			window:        16000,
			wantReserve:   2048,
			wantSafety:    1024,
			wantEffective: 12928,
			wantSoft:      0.65,
			wantForce:     0.75,
			wantEmergency: 0.84,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reserve := resolveReserveOutputTokens(tt.window, 0)
			if reserve != tt.wantReserve {
				t.Fatalf("reserve = %d, want %d", reserve, tt.wantReserve)
			}
			safety := resolveSafetyMarginTokens(tt.window, 0)
			if safety != tt.wantSafety {
				t.Fatalf("safety = %d, want %d", safety, tt.wantSafety)
			}
			if got := resolveEffectiveInputBudget(tt.window, reserve, safety); got != tt.wantEffective {
				t.Fatalf("effective budget = %d, want %d", got, tt.wantEffective)
			}
			soft, force := dynamicWatermarks(tt.window, 0, 0)
			if soft != tt.wantSoft || force != tt.wantForce {
				t.Fatalf("watermarks = %.2f/%.2f, want %.2f/%.2f", soft, force, tt.wantSoft, tt.wantForce)
			}
			if got := dynamicEmergencyWatermark(tt.window, 0); got != tt.wantEmergency {
				t.Fatalf("emergency = %.2f, want %.2f", got, tt.wantEmergency)
			}
		})
	}
}

func TestEvaluateWatermarkUsesSharedThresholds(t *testing.T) {
	t.Parallel()

	cfg := CompactionConfig{}
	base := compact.UsageSnapshot{
		ContextWindowTokens:  128000,
		EffectiveInputBudget: 1000,
	}
	tests := []struct {
		name       string
		total      int
		want       bool
		wantReason string
	}{
		{name: "below", total: 819},
		{name: "soft", total: 820, want: true, wantReason: "context_watermark"},
		{name: "force", total: 900, want: true, wantReason: "context_limit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := base
			usage.TotalTokens = tt.total
			got := evaluateWatermark(usage, cfg)
			if got.ShouldCompact != tt.want || got.Reason != tt.wantReason {
				t.Fatalf("evaluateWatermark(%d) = %+v, want compact=%v reason=%q", tt.total, got, tt.want, tt.wantReason)
			}
		})
	}
}

func TestUsageForModelRequestInflatesProviderUsageWithRequestEstimate(t *testing.T) {
	t.Parallel()

	assistant := assistantEvent("provider baseline")
	assistant.ID = "a1"
	assistant.Meta = map[string]any{
		"prompt_tokens":     10,
		"completion_tokens": 1,
		"total_tokens":      11,
	}
	req := &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "request text that is deliberately much larger than the provider baseline"),
		},
	}

	user := userTextEvent("hello")
	user.ID = "u1"
	usage, requestTokens := usageForModelRequest([]*session.Event{user, assistant}, staticModel{text: "ok"}, req, CompactionConfig{
		DefaultContextWindowTokens: 1000,
	})

	if requestTokens <= 11 {
		t.Fatalf("request tokens = %d, want larger than provider baseline", requestTokens)
	}
	if usage.TotalTokens != requestTokens {
		t.Fatalf("usage total = %d, want request estimate %d", usage.TotalTokens, requestTokens)
	}
	if usage.Source != compact.UsageSourceProvider {
		t.Fatalf("usage source = %q, want provider baseline source preserved", usage.Source)
	}
}

func TestEstimateModelRequestTokensIncludesStructuredRequestParts(t *testing.T) {
	t.Parallel()

	toolInput := json.RawMessage(`{"query":"latest release"}`)
	req := &model.Request{
		Instructions: []model.Part{
			model.NewTextPart("follow the tool result"),
			model.NewMediaPart(model.MediaModalityImage, model.MediaSource{
				Kind: model.MediaSourceInline,
				Data: "inline-image-data",
			}, "image/png", "screenshot.png"),
			model.NewJSONPart(json.RawMessage(`{"mode":"strict"}`)),
		},
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "search first"),
			model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
				ID:   "call-1",
				Name: "SEARCH",
				Args: string(toolInput),
			}}, ""),
			model.MessageFromToolResponse(&model.ToolResponse{
				ID:     "call-1",
				Name:   "SEARCH",
				Result: map[string]any{"result": "found source"},
			}),
		},
		Tools: []model.ToolSpec{
			model.NewFunctionToolSpec("SEARCH", "search docs", map[string]any{"type": "object"}),
		},
		Output: &model.OutputSpec{
			Mode:            model.OutputModeJSON,
			JSONSchema:      map[string]any{"type": "object"},
			MaxOutputTokens: 200,
		},
	}

	got := estimateModelRequestTokens(req)
	wantAtLeast := estimateTextTokens("follow the tool result") +
		estimateTextTokens("inline-image-data") +
		estimateTextTokens("search first") +
		estimateTextTokens("SEARCH") +
		estimateTextTokens(string(toolInput)) +
		estimateTextTokens("found source")
	if got < wantAtLeast {
		t.Fatalf("estimateModelRequestTokens() = %d, want at least %d", got, wantAtLeast)
	}
}

func TestEstimateModelRequestTokensIncludesStructuredMessageParts(t *testing.T) {
	t.Parallel()

	inlineData := strings.Repeat("inline-image-data-", 24)
	visibleReasoning := strings.Repeat("visible reasoning ", 16)
	jsonPayload := json.RawMessage(`{"payload":"large structured message body","items":["alpha","beta","gamma"]}`)
	req := &model.Request{
		Messages: []model.Message{
			model.NewMessage(model.RoleUser,
				model.NewMediaPart(model.MediaModalityImage, model.MediaSource{
					Kind: model.MediaSourceInline,
					Data: inlineData,
				}, "image/png", "screenshot.png"),
				model.NewJSONPart(jsonPayload),
				model.NewFileRefPart("report.pdf", "application/pdf", "https://example.com/report.pdf", "file-123", "local-report-ref"),
				model.NewReasoningPart(visibleReasoning, model.ReasoningVisibilityVisible),
			),
		},
	}

	got := estimateModelRequestTokens(req)
	wantAtLeast := estimateTextTokens(inlineData) +
		estimateTextTokens(string(jsonPayload)) +
		estimateTextTokens("report.pdf") +
		estimateTextTokens("https://example.com/report.pdf") +
		estimateTextTokens("file-123") +
		estimateTextTokens("local-report-ref") +
		estimateTextTokens(visibleReasoning)
	if got < wantAtLeast {
		t.Fatalf("estimateModelRequestTokens() = %d, want at least %d", got, wantAtLeast)
	}
}
