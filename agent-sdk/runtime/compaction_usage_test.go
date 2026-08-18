package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/prefixusage"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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

func TestComputeUsageSnapshotForModelIgnoresNewerDifferentModelBaseline(t *testing.T) {
	t.Parallel()

	assistantEvent := func(id string, provider string, modelName string, promptTokens int) *session.Event {
		message := model.NewTextMessage(model.RoleAssistant, "answer")
		return &session.Event{
			ID:         id,
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &message,
			Text:       message.TextContent(),
			Invocation: &session.EventInvocation{Provider: provider, Model: modelName},
			Meta: map[string]any{
				"prompt_tokens":     promptTokens,
				"completion_tokens": 5,
				"total_tokens":      promptTokens + 5,
			},
		}
	}
	events := []*session.Event{
		assistantEvent("main-usage", "openai-codex", "gpt-main", 80_000),
		assistantEvent("guardian-usage", "deepseek", "guardian", 25_941),
	}

	got := ComputeUsageSnapshotForModel(
		events,
		nil,
		258_400,
		CompactionConfig{},
		"openai-codex",
		"gpt-main",
	)

	if got.Source != compact.UsageSourceProvider || got.AsOfEventID != "main-usage" {
		t.Fatalf("usage = %+v, want main model provider baseline", got)
	}
	if got.TotalTokens < 80_000 || got.ContextWindowTokens != 258_400 {
		t.Fatalf("usage = %+v, want main model tokens and context window", got)
	}
}

func TestComputeUsageSnapshotForModelUsesRecordedRequestPrefixWithoutProviderUsage(t *testing.T) {
	t.Parallel()

	user := model.NewTextMessage(model.RoleUser, "hello")
	assistant := model.NewTextMessage(model.RoleAssistant, "answer")
	events := []*session.Event{
		{
			ID:         "u1",
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Message:    &user,
		},
		{
			ID:         "a1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &assistant,
			Invocation: &session.EventInvocation{
				Provider:                "openai-codex",
				Model:                   "gpt-main",
				PromptPrefixFingerprint: "sha256:actual-request",
				PromptPrefixTokens:      777,
			},
		},
	}

	got := ComputeUsageSnapshotForModel(
		events,
		nil,
		258_400,
		CompactionConfig{EstimatedPromptPrefixTokens: 12_345},
		"openai-codex",
		"gpt-main",
	)

	if got.Source != compact.UsageSourceEstimated {
		t.Fatalf("usage source = %q, want estimated without provider usage", got.Source)
	}
	if got.EstimatedPrefixTokens != 777 {
		t.Fatalf("estimated prefix = %d, want last actual request prefix 777", got.EstimatedPrefixTokens)
	}
	if got.TotalTokens <= got.EstimatedPrefixTokens {
		t.Fatalf("usage = %+v, want history plus request prefix", got)
	}
}

func TestComputeUsageSnapshotForModelKeepsRecordedPrefixAcrossCompact(t *testing.T) {
	t.Parallel()

	assistant := model.NewTextMessage(model.RoleAssistant, "answer")
	compactMessage := model.NewTextMessage(model.RoleUser, "CONTEXT CHECKPOINT\n\nObjective:\n- keep working")
	events := []*session.Event{
		{
			ID:         "a1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &assistant,
			Invocation: &session.EventInvocation{
				Provider:           "openai-codex",
				Model:              "gpt-main",
				PromptPrefixTokens: 777,
			},
		},
		{
			ID:         "compact-1",
			Type:       session.EventTypeCompact,
			Actor:      session.ActorRef{Kind: session.ActorKindSystem},
			Visibility: session.VisibilityCanonical,
			Message:    &compactMessage,
		},
	}

	got := ComputeUsageSnapshotForModel(
		events,
		nil,
		258_400,
		CompactionConfig{EstimatedPromptPrefixTokens: 12_345},
		"openai-codex",
		"gpt-main",
	)
	if got.EstimatedPrefixTokens != 777 {
		t.Fatalf("estimated prefix = %d, want last request prefix 777 across compact", got.EstimatedPrefixTokens)
	}
}

func TestComputeUsageSnapshotForModelDoesNotReuseDifferentModelRequestPrefix(t *testing.T) {
	t.Parallel()

	assistant := model.NewTextMessage(model.RoleAssistant, "answer")
	events := []*session.Event{{
		ID:         "guardian-a1",
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &assistant,
		Invocation: &session.EventInvocation{
			Provider:           "deepseek",
			Model:              "guardian",
			PromptPrefixTokens: 999,
		},
	}}

	got := ComputeUsageSnapshotForModel(
		events,
		nil,
		258_400,
		CompactionConfig{EstimatedPromptPrefixTokens: 321},
		"openai-codex",
		"gpt-main",
	)
	if got.EstimatedPrefixTokens != 321 {
		t.Fatalf("estimated prefix = %d, want static fallback 321", got.EstimatedPrefixTokens)
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
			wantReserve:   32000,
			wantSafety:    8000,
			wantEffective: 960000,
			wantSoft:      0.99,
			wantForce:     0.995,
			wantEmergency: 0.998,
		},
		{
			name:          "200k",
			window:        200000,
			wantReserve:   8000,
			wantSafety:    2048,
			wantEffective: 189952,
			wantSoft:      0.95,
			wantForce:     0.98,
			wantEmergency: 0.99,
		},
		{
			name:          "128k",
			window:        128000,
			wantReserve:   4096,
			wantSafety:    1536,
			wantEffective: 122368,
			wantSoft:      0.90,
			wantForce:     0.94,
			wantEmergency: 0.97,
		},
		{
			name:          "64k",
			window:        64000,
			wantReserve:   4096,
			wantSafety:    1536,
			wantEffective: 58368,
			wantSoft:      0.88,
			wantForce:     0.93,
			wantEmergency: 0.96,
		},
		{
			name:          "32k",
			window:        32000,
			wantReserve:   2048,
			wantSafety:    1024,
			wantEffective: 28928,
			wantSoft:      0.82,
			wantForce:     0.90,
			wantEmergency: 0.94,
		},
		{
			name:          "small",
			window:        16000,
			wantReserve:   2048,
			wantSafety:    1024,
			wantEffective: 12928,
			wantSoft:      0.78,
			wantForce:     0.88,
			wantEmergency: 0.92,
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

func TestDynamicCompactionDefaultsFor112KTriggerNear85PercentRaw(t *testing.T) {
	t.Parallel()

	const (
		window       = 112000
		wantSoftRaw  = 0.855
		wantForceRaw = 0.892
		tolerance    = 0.01
	)
	reserve := resolveReserveOutputTokens(window, 0)
	safety := resolveSafetyMarginTokens(window, 0)
	effective := resolveEffectiveInputBudget(window, reserve, safety)
	soft, force := dynamicWatermarks(window, 0, 0)

	softRaw := float64(effective) * soft / float64(window)
	forceRaw := float64(effective) * force / float64(window)
	if diff := absFloat64(softRaw - wantSoftRaw); diff > tolerance {
		t.Fatalf("112k soft raw trigger = %.4f, want %.4f +/- %.4f", softRaw, wantSoftRaw, tolerance)
	}
	if diff := absFloat64(forceRaw - wantForceRaw); diff > tolerance {
		t.Fatalf("112k force raw trigger = %.4f, want %.4f +/- %.4f", forceRaw, wantForceRaw, tolerance)
	}
}

func absFloat64(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
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
		{name: "below", total: 899},
		{name: "soft", total: 900, want: true, wantReason: "context_watermark"},
		{name: "force", total: 940, want: true, wantReason: "context_limit"},
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

func TestUsageForModelRequestKeepsProviderUsageAuthoritative(t *testing.T) {
	t.Parallel()

	req := &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, strings.Repeat("request text ", 64)),
		},
	}
	prefix := prefixusage.ForRequest(req)
	assistant := assistantEvent("provider baseline")
	assistant.ID = "a1"
	assistant.Invocation = &session.EventInvocation{
		Provider:                "openai-codex",
		Model:                   "gpt-5.6-sol",
		PromptPrefixFingerprint: prefix.Fingerprint,
		PromptPrefixTokens:      prefix.Tokens,
	}
	assistant.Meta = map[string]any{
		"prompt_tokens":     10,
		"completion_tokens": 1,
		"total_tokens":      11,
	}

	user := userTextEvent("hello")
	user.ID = "u1"
	fresh := userTextEvent("fresh post-snapshot event")
	fresh.ID = "u2"
	usage, requestTokens := usageForModelRequest([]*session.Event{user, assistant, fresh}, identifiedCompactionModel{
		staticModel:  staticModel{text: "ok"},
		providerName: "openai-codex",
		modelName:    "gpt-5.6-sol",
	}, req, CompactionConfig{
		DefaultContextWindowTokens: 1000,
	})

	if requestTokens <= 11 {
		t.Fatalf("request tokens = %d, want larger than provider baseline", requestTokens)
	}
	if usage.TotalTokens >= requestTokens {
		t.Fatalf("usage total = %d, want provider baseline below local request estimate %d", usage.TotalTokens, requestTokens)
	}
	if usage.Source != compact.UsageSourceProvider {
		t.Fatalf("usage source = %q, want provider baseline source preserved", usage.Source)
	}
	if usage.EstimatedDeltaTokens == 0 || usage.TotalTokens <= 10 {
		t.Fatalf("usage = %+v, want provider baseline plus estimated post-snapshot delta", usage)
	}
}

func TestUsageForModelRequestAdjustsOnlyChangedPrefix(t *testing.T) {
	t.Parallel()

	previousRequest := &model.Request{
		Instructions: []model.Part{model.NewTextPart("short system prompt")},
	}
	previousPrefix := prefixusage.ForRequest(previousRequest)
	assistant := assistantEvent("provider baseline")
	assistant.ID = "a1"
	assistant.Invocation = &session.EventInvocation{
		Provider:                "openai-codex",
		Model:                   "gpt-5.6-sol",
		PromptPrefixFingerprint: previousPrefix.Fingerprint,
		PromptPrefixTokens:      previousPrefix.Tokens,
	}
	assistant.Meta = map[string]any{
		"prompt_tokens":     100000,
		"completion_tokens": 1,
		"total_tokens":      100001,
	}
	currentRequest := &model.Request{
		Instructions: []model.Part{model.NewTextPart(strings.Repeat("expanded system prompt ", 4000))},
		Messages: []model.Message{
			model.NewMessage(model.RoleUser,
				model.NewMediaPart(model.MediaModalityImage, model.MediaSource{
					Kind: model.MediaSourceInline,
					Data: strings.Repeat("A", 1<<20),
				}, "image/png", "attachment.png"),
			),
		},
	}
	currentPrefix := prefixusage.ForRequest(currentRequest)
	modelIdentity := identifiedCompactionModel{
		staticModel:  staticModel{text: "ok"},
		providerName: "openai-codex",
		modelName:    "gpt-5.6-sol",
	}
	baseUsage := snapshotUsageWithResolvedWindowUsing(
		[]*session.Event{assistant},
		258400,
		CompactionConfig{},
		func(snapshot providerTokenSnapshot) bool {
			return providerSnapshotCompatibleWithLLM(snapshot, modelIdentity)
		},
	)

	usage, requestTokens, prefixChanged := usageForModelRequestDetails(
		[]*session.Event{assistant},
		modelIdentity,
		currentRequest,
		CompactionConfig{DefaultContextWindowTokens: 258400},
	)

	if !prefixChanged {
		t.Fatal("prefixChanged = false, want changed request prefix detected")
	}
	want := baseUsage.TotalTokens + currentPrefix.Tokens - previousPrefix.Tokens
	if usage.TotalTokens != want {
		t.Fatalf("usage total = %d, want provider total plus prefix delta %d", usage.TotalTokens, want)
	}
	if requestTokens < estimatedImageMediaTokens {
		t.Fatalf("request estimate = %d, want bounded attachment budget retained for estimated-only paths", requestTokens)
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
				Name: "Grep",
				Args: string(toolInput),
			}}, ""),
			model.MessageFromToolResponse(&model.ToolResponse{
				ID:     "call-1",
				Name:   "Grep",
				Result: map[string]any{"result": "found source"},
			}),
		},
		Tools: []model.ToolSpec{
			model.NewFunctionToolSpec("Grep", "search docs", map[string]any{"type": "object"}),
		},
		Output: &model.OutputSpec{
			Mode:            model.OutputModeJSON,
			JSONSchema:      map[string]any{"type": "object"},
			MaxOutputTokens: 200,
		},
	}

	got := estimateModelRequestTokens(req)
	wantAtLeast := estimateTextTokens("follow the tool result") +
		estimateMediaPartTokens(req.Instructions[1].Media) +
		estimateTextTokens("search first") +
		estimateTextTokens("Grep") +
		estimateTextTokens(string(toolInput)) +
		estimateTextTokens("found source")
	if got < wantAtLeast {
		t.Fatalf("estimateModelRequestTokens() = %d, want at least %d", got, wantAtLeast)
	}
}

func TestEstimateModelRequestTokensBoundsInlineMediaPayload(t *testing.T) {
	t.Parallel()

	visibleReasoning := strings.Repeat("visible reasoning ", 16)
	jsonPayload := json.RawMessage(`{"payload":"large structured message body","items":["alpha","beta","gamma"]}`)
	requestWithMedia := func(data string) *model.Request {
		return &model.Request{
			Messages: []model.Message{
				model.NewMessage(model.RoleUser,
					model.NewMediaPart(model.MediaModalityImage, model.MediaSource{
						Kind: model.MediaSourceInline,
						Data: data,
					}, "image/png", "screenshot.png"),
					model.NewJSONPart(jsonPayload),
					model.NewFileRefPart("report.pdf", "application/pdf", "https://example.com/report.pdf", "file-123", "local-report-ref"),
					model.NewReasoningPart(visibleReasoning, model.ReasoningVisibilityVisible),
				),
			},
		}
	}

	small := estimateModelRequestTokens(requestWithMedia("aW1n"))
	large := estimateModelRequestTokens(requestWithMedia(strings.Repeat("A", 1<<20)))
	if large != small {
		t.Fatalf("large inline media estimate = %d, want bounded estimate %d independent of base64 length", large, small)
	}
	wantAtLeast := estimatedImageMediaTokens +
		estimateTextTokens(string(jsonPayload)) +
		estimateTextTokens("report.pdf") +
		estimateTextTokens("https://example.com/report.pdf") +
		estimateTextTokens("file-123") +
		estimateTextTokens("local-report-ref") +
		estimateTextTokens(visibleReasoning)
	if large < wantAtLeast {
		t.Fatalf("estimateModelRequestTokens() = %d, want at least %d", large, wantAtLeast)
	}
}

func TestEstimateModelRequestTokensChargesEachInlineImage(t *testing.T) {
	t.Parallel()

	one := estimateModelRequestTokens(&model.Request{Messages: []model.Message{
		model.NewMessage(model.RoleUser, model.NewMediaPart(model.MediaModalityImage, model.MediaSource{
			Kind: model.MediaSourceInline,
			Data: strings.Repeat("A", 1<<20),
		}, "image/png", "one.png")),
	}})
	three := estimateModelRequestTokens(&model.Request{Messages: []model.Message{
		model.NewMessage(model.RoleUser,
			model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: strings.Repeat("A", 403992)}, "image/png", "one.png"),
			model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: strings.Repeat("B", 175800)}, "image/png", "two.png"),
			model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: strings.Repeat("C", 388608)}, "image/png", "three.png"),
		),
	}})

	if one < estimatedImageMediaTokens {
		t.Fatalf("one image estimate = %d, want at least %d", one, estimatedImageMediaTokens)
	}
	if three < estimatedImageMediaTokens*3 {
		t.Fatalf("three image estimate = %d, want at least %d", three, estimatedImageMediaTokens*3)
	}
	if three >= 50000 {
		t.Fatalf("three image estimate = %d, want bounded attachment estimate below 50000", three)
	}
}

func TestEvaluateModelRequestBudgetUsesOneFullyAssembledRequest(t *testing.T) {
	t.Parallel()

	req := &model.Request{
		Instructions: []model.Part{model.NewTextPart("fixed guardian policy")},
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "historical request"),
			model.NewTextMessage(model.RoleAssistant, "prior assessment"),
			model.NewTextMessage(model.RoleUser, strings.Repeat("exact approval ", 160)),
		},
		Tools: []model.ToolSpec{model.NewFunctionToolSpec(
			"bounded_tool",
			"model-visible tool definition",
			map[string]any{"type": "object"},
		)},
		Output: &model.OutputSpec{
			Mode:            model.OutputModeSchema,
			JSONSchema:      map[string]any{"type": "object", "required": []any{"outcome"}},
			MaxOutputTokens: 256,
		},
	}
	cfg := CompactionConfig{
		DefaultContextWindowTokens:  4_096,
		ReserveOutputTokens:         512,
		SafetyMarginTokens:          256,
		EstimatedPromptPrefixTokens: 99_999,
	}

	got := EvaluateModelRequestBudget(nil, req, cfg)
	wantTokens := estimateModelRequestTokens(req)
	if got.Usage.TotalTokens != wantTokens {
		t.Fatalf("TotalTokens = %d, want exact request estimate %d", got.Usage.TotalTokens, wantTokens)
	}
	wantEffective := resolveEffectiveInputBudget(4_096, 512, 256)
	if got.Usage.EffectiveInputBudget != wantEffective {
		t.Fatalf("EffectiveInputBudget = %d, want %d", got.Usage.EffectiveInputBudget, wantEffective)
	}
	if got.Usage.EstimatedPrefixTokens != 0 {
		t.Fatalf("EstimatedPrefixTokens = %d, want no separately added prefix", got.Usage.EstimatedPrefixTokens)
	}
	if got.Compaction != evaluateWatermark(got.Usage, cfg) {
		t.Fatalf("Compaction = %#v, want Runtime watermark result %#v", got.Compaction, evaluateWatermark(got.Usage, cfg))
	}
}
