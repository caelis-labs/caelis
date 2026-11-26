package runtime

import (
	"encoding/json"
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/prefixusage"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSnapshotUsageIncludesEstimatedPromptPrefix(t *testing.T) {
	msg := model.NewTextMessage(model.RoleUser, "hello")
	events := []*session.Event{{
		ID:         "u1",
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Message:    &msg,
		Text:       msg.TextContent(),
	}}

	got := snapshotUsageWithResolvedWindow(compact.PromptEventsFromLatestCompact(events), 1000, CompactionConfig{
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

func TestSnapshotUsageDoesNotDoubleCountPrefixWithProviderBaseline(t *testing.T) {
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

	got := snapshotUsageWithResolvedWindow(compact.PromptEventsFromLatestCompact(events), 1000, CompactionConfig{
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

func TestSnapshotUsageIncludesAnthropicCachedInputBaseline(t *testing.T) {
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

	got := snapshotUsageWithResolvedWindow(compact.PromptEventsFromLatestCompact(events), 1048576, CompactionConfig{})

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

	got := computeUsageSnapshotForModel(
		events,
		258_400,
		CompactionConfig{},
		"openai-codex",
		"gpt-main",
		estimatePromptEventTokens,
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

	got := computeUsageSnapshotForModel(
		events,
		258_400,
		CompactionConfig{EstimatedPromptPrefixTokens: 12_345},
		"openai-codex",
		"gpt-main",
		estimatePromptEventTokens,
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

	got := computeUsageSnapshotForModel(
		events,
		258_400,
		CompactionConfig{EstimatedPromptPrefixTokens: 12_345},
		"openai-codex",
		"gpt-main",
		estimatePromptEventTokens,
	)
	if got.EstimatedPrefixTokens != 777 {
		t.Fatalf("estimated prefix = %d, want last request prefix 777 across compact", got.EstimatedPrefixTokens)
	}
}

func TestProviderSnapshotGroupStopsAtCheckpoint(t *testing.T) {
	t.Parallel()

	usage := map[string]any{"prompt_tokens": 500, "model": "fixture", "provider": "fixture"}
	retained := &session.Event{ID: "retained", Seq: 2, Meta: usage}
	fresh := &session.Event{ID: "fresh", Seq: 4, Meta: usage}
	snapshot, ok := latestProviderTokenSnapshotUsing([]*session.Event{retained, fresh}, 3, nil)
	if !ok || snapshot.BaselineTokens != 500 || snapshot.EventID != fresh.ID || snapshot.DeltaStartIndex != 1 {
		t.Fatalf("equal usage amounts grouped across the checkpoint: %+v", snapshot)
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

	got := computeUsageSnapshotForModel(
		events,
		258_400,
		CompactionConfig{EstimatedPromptPrefixTokens: 321},
		"openai-codex",
		"gpt-main",
		estimatePromptEventTokens,
	)
	if got.EstimatedPrefixTokens != 321 {
		t.Fatalf("estimated prefix = %d, want static fallback 321", got.EstimatedPrefixTokens)
	}
}

func TestSol61UsableWindowCompactionThresholds(t *testing.T) {
	t.Parallel()
	usage := snapshotUsageWithResolvedWindow(nil, 475000, CompactionConfig{})
	if usage.ContextWindowTokens != 475000 || usage.EffectiveInputBudget != 458109 {
		t.Fatalf("usable-window budget = %#v", usage)
	}
	for _, tc := range []struct {
		tokens    int
		reason    string
		emergency bool
	}{
		{tokens: 435203},
		{tokens: 435204, reason: "context_watermark"},
		{tokens: 448946, reason: "context_watermark"},
		{tokens: 448947, reason: "context_limit"},
		{tokens: 453527, reason: "context_limit"},
		{tokens: 453528, reason: "context_limit", emergency: true},
	} {
		usage.TotalTokens = tc.tokens
		decision := evaluateWatermark(usage, CompactionConfig{})
		if decision.Reason != tc.reason || decision.ShouldCompact != (tc.reason != "") ||
			evaluateEmergencyWatermark(usage, CompactionConfig{}) != tc.emergency {
			t.Fatalf("tokens=%d: compaction=%#v, want reason=%q emergency=%t", tc.tokens, decision, tc.reason, tc.emergency)
		}
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
			name:          "sol 6.1 usable window",
			window:        475000,
			wantReserve:   14843,
			wantSafety:    2048,
			wantEffective: 458109,
			wantSoft:      0.95,
			wantForce:     0.98,
			wantEmergency: 0.99,
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
	usage, _ := usageForModelRequest([]*session.Event{user, assistant, fresh}, identifiedCompactionModel{
		staticModel:  staticModel{text: "ok"},
		providerName: "openai-codex",
		modelName:    "gpt-5.6-sol",
	}, req, CompactionConfig{
		DefaultContextWindowTokens: 1000,
	})

	requestTokens := estimateModelRequestTokens(req)
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
		0,
		func(snapshot providerTokenSnapshot) bool {
			return providerSnapshotCompatibleWithLLM(snapshot, modelIdentity)
		},
	)

	usage, prefixChanged := usageForModelRequest(
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
	if requestTokens := estimateModelRequestTokens(currentRequest); requestTokens < prefixusage.EstimatedImageMediaTokens {
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
			model.NewMessage(model.RoleTool, model.NewToolResultJSONPart("call-1", "Grep", map[string]any{"result": "found source"}, false)),
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
					model.Part{Kind: model.PartKindFileRef, FileRef: &model.FileRefPart{Name: "report.pdf", MimeType: "application/pdf", URI: "https://example.com/report.pdf", FileID: "file-123", LocalRef: "local-report-ref"}},
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
	wantAtLeast := prefixusage.EstimatedImageMediaTokens +
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

	if one < prefixusage.EstimatedImageMediaTokens {
		t.Fatalf("one image estimate = %d, want at least %d", one, prefixusage.EstimatedImageMediaTokens)
	}
	if three < prefixusage.EstimatedImageMediaTokens*3 {
		t.Fatalf("three image estimate = %d, want at least %d", three, prefixusage.EstimatedImageMediaTokens*3)
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
	if got.TotalTokens != wantTokens {
		t.Fatalf("TotalTokens = %d, want exact request estimate %d", got.TotalTokens, wantTokens)
	}
	wantEffective := resolveEffectiveInputBudget(4_096, 512, 256)
	if got.EffectiveInputBudget != wantEffective {
		t.Fatalf("EffectiveInputBudget = %d, want %d", got.EffectiveInputBudget, wantEffective)
	}
	if got.EstimatedPrefixTokens != 0 {
		t.Fatalf("EstimatedPrefixTokens = %d, want no separately added prefix", got.EstimatedPrefixTokens)
	}
}

func TestAPICacheAccountingSurvivesContextRoundTrip(t *testing.T) {
	for _, route := range []struct {
		api      model.APIType
		provider string
	}{
		{model.APIAnthropic, "anthropic"},
		{model.APIAnthropicCompatible, "custom"},
		{model.APIOpenAI, "openai"},
		{model.APIOpenAICodex, "openai-codex"},
		{model.APIOpenAIResponses, "custom"},
		// API accounting must override even a conflicting provider label.
		{model.APIOpenAIResponses, "anthropic"},
	} {
		for _, phase := range []string{"cold", "warm", "zero", "missing"} {
			t.Run(fmt.Sprintf("%s/%s/%s", route.api, route.provider, phase), func(t *testing.T) {
				input, cached, written := 9000, 0, 0
				if phase == "warm" {
					cached = 25000
				}
				anthropic := route.api == model.APIAnthropic || route.api == model.APIAnthropicCompatible
				if phase == "cold" && anthropic {
					written = 25000
				}
				wantPrompt := input + cached + written
				rawUsage := map[string]any{"input_tokens": input, "output_tokens": 50}
				if anthropic && phase != "missing" {
					rawUsage["cache_read_input_tokens"] = cached
					rawUsage["cache_creation_input_tokens"] = written
					rawUsage["cache_creation"] = map[string]int{"ephemeral_5m_input_tokens": written * 3 / 5, "ephemeral_1h_input_tokens": written * 2 / 5}
				} else if !anthropic {
					rawUsage["input_tokens"], rawUsage["total_tokens"] = wantPrompt, wantPrompt+50
					if phase != "missing" {
						rawUsage["input_tokens_details"] = map[string]int{"cached_tokens": cached}
					}
				}
				raw, _ := json.Marshal(rawUsage)
				wire := fmt.Sprintf(`{"id":"fixture","type":"message","role":"assistant","model":"fixture","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":%s}`, raw)
				if !anthropic {
					wire = fmt.Sprintf(`{"id":"fixture","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":%s}`, raw)
				}
				auth := providers.AuthConfig{Type: model.AuthAPIKey, Token: "fixture"}
				contentType := "application/json"
				if route.api == model.APIOpenAICodex {
					auth = providers.AuthConfig{Type: model.AuthOAuthToken}
					contentType = "text/event-stream"
					wire = `data: {"type":"response.completed","response":` + wire + "}\n\n"
				}
				calls := 0
				transport := compactionTerminalTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(wire)), Request: req}, nil
				})
				llm, err := providers.New(providers.Config{API: route.api, Provider: route.provider, Model: "fixture", BaseURL: "http://fixture.invalid", Auth: auth, HTTPClient: &http.Client{Transport: transport}})
				if err != nil {
					t.Fatal(err)
				}
				var usage model.Usage
				for response, err := range llm.Generate(t.Context(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "source")}}) {
					if err != nil {
						t.Fatal(err)
					}
					if response != nil && response.Usage.IsReported() {
						usage = response.Usage
					}
				}
				if calls != 1 || usage.TotalTokens != wantPrompt+50 {
					t.Fatalf("calls=%d usage=%+v", calls, usage)
				}
				event := assistantEvent("answer")
				event.ID = "a1"
				event.Meta = map[string]any{"caelis": map[string]any{"sdk": map[string]any{"provider": route.provider, "model": "fixture", "usage": session.ModelUsageMetadata(route.provider, usage)}}}
				raw, err = json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				var restored session.Event
				if err := json.Unmarshal(raw, &restored); err != nil {
					t.Fatal(err)
				}
				gauge := snapshotUsageWithResolvedWindow(compact.PromptEventsFromLatestCompact([]*session.Event{userTextEvent("source"), &restored}), 100000, CompactionConfig{})
				if gauge.TotalTokens < wantPrompt || gauge.TotalTokens >= wantPrompt+100 {
					t.Errorf("context=%d want input baseline %d plus short output", gauge.TotalTokens, wantPrompt)
				}
				measured := session.UsageSnapshotFromSessionEvent(&restored)
				if measured == nil {
					t.Fatal("missing measured usage")
				}
				raw, err = json.Marshal(measured)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, measured); err != nil {
					t.Fatal(err)
				}
				if measured.PromptTokens != usage.PromptTokens || measured.TotalTokens != wantPrompt+50 || measured.CachedInputTokens != cached || measured.CacheWriteTokens != written || measured.CacheReadAccounting != usage.CacheReadAccounting {
					t.Errorf("usage after Session round trip=%+v, provider usage=%+v", measured, usage)
				}
				if measured.CachedInputTokensReported != (phase != "missing") || measured.CacheWriteTokensReported != (anthropic && phase != "missing") {
					t.Fatalf("cache presence changed: %+v", measured)
				}
			})
		}
	}
}

func TestCompactionFullCacheReadBaselineCountsReadOnce(t *testing.T) {
	for _, provider := range []string{"anthropic", "deepseek", "minimax"} {
		for _, nested := range []bool{false, true} {
			usage := map[string]any{"prompt_tokens": 0, "cached_input_tokens": 6000, "completion_tokens": 20, "total_tokens": 6020, "cache_write_tokens": 0}
			meta := map[string]any{"provider": provider}
			if nested {
				meta["caelis"] = map[string]any{"sdk": map[string]any{"provider": provider, "usage": usage}}
			} else {
				for k, v := range usage {
					meta[k] = v
				}
			}
			got, input, ok := providerPromptBaselineTokens(meta)
			if !ok || !input || got != 6000 {
				t.Fatalf("provider %s nested %v baseline=%d input=%v ok=%v", provider, nested, got, input, ok)
			}
		}
	}
}

func TestCompactionPrefixSafe(t *testing.T) {
	calls := func(ids ...string) model.Message {
		parts := make([]model.Part, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, model.NewToolUsePart(id, "lookup", nil))
		}
		return model.NewMessage(model.RoleAssistant, parts...)
	}
	results := func(ids ...string) model.Message {
		parts := make([]model.Part, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, model.NewToolResultJSONPart(id, "lookup", nil, false))
		}
		return model.NewMessage(model.RoleTool, parts...)
	}
	withRole := func(message model.Message, role model.Role) model.Message {
		message.Role = role
		return message
	}
	user := model.NewTextMessage(model.RoleUser, "continue")
	assistant := model.NewTextMessage(model.RoleAssistant, "done")
	mixed := model.NewMessage(model.RoleAssistant, append(calls("a").Parts, results("a").Parts...)...)
	resultThenCall := model.NewMessage(model.RoleAssistant, append(results("a").Parts, calls("b").Parts...)...)
	callWithText := model.NewMessage(model.RoleAssistant, append([]model.Part{model.NewTextPart("looking up")}, calls("a").Parts...)...)

	for _, tt := range []struct {
		name     string
		messages []model.Message
		want     bool
	}{
		{"empty", nil, true},
		{"ordinary conversation", []model.Message{user, assistant}, true},
		{"single closed batch", []model.Message{user, calls("a"), results("a")}, true},
		{"conversation after batch", []model.Message{calls("a"), results("a"), assistant, user}, true},
		{"assistant text with calls", []model.Message{callWithText, results("a")}, true},
		{"parallel grouped results", []model.Message{calls("a", "b"), results("a", "b")}, true},
		{"parallel out of order results", []model.Message{calls("a", "b"), results("b"), results("a")}, true},
		{"parallel grouped out of order", []model.Message{calls("a", "b"), results("b", "a")}, true},
		{"separate batches", []model.Message{calls("a"), results("a"), calls("b"), results("b")}, true},
		{"reused ID after closed batch", []model.Message{calls("a"), results("a"), calls("a"), results("a")}, true},
		{"reused parallel IDs", []model.Message{calls("a", "b"), results("b", "a"), calls("b", "a"), results("a", "b")}, true},
		{"IDs are not trimmed", []model.Message{calls(" a", "a"), results("a", " a")}, true},
		{"trailing call", []model.Message{calls("a")}, false},
		{"trailing parallel calls", []model.Message{calls("a", "b")}, false},
		{"partially completed parallel calls", []model.Message{calls("a", "b"), results("a")}, false},
		{"orphan result", []model.Message{results("a")}, false},
		{"orphan after closed batch", []model.Message{calls("a"), results("a"), results("b")}, false},
		{"unmatched result", []model.Message{calls("a"), results("b")}, false},
		{"empty call ID", []model.Message{calls(""), results("")}, false},
		{"empty result ID", []model.Message{calls("a"), results("")}, false},
		{"duplicate call IDs", []model.Message{calls("a", "a"), results("a")}, false},
		{"duplicate result in message", []model.Message{calls("a", "b"), results("a", "a", "b")}, false},
		{"duplicate result across messages", []model.Message{calls("a", "b"), results("a"), results("a"), results("b")}, false},
		{"duplicate result after batch", []model.Message{calls("a"), results("a"), results("a")}, false},
		{"new calls before results", []model.Message{calls("a"), calls("b"), results("a", "b")}, false},
		{"new calls during parallel batch", []model.Message{calls("a", "b"), results("a"), calls("c"), results("b", "c")}, false},
		{"reuse before batch closed", []model.Message{calls("a", "b"), results("a"), calls("a"), results("b", "a")}, false},
		{"user interrupts batch", []model.Message{calls("a"), user, results("a")}, false},
		{"assistant interrupts batch", []model.Message{calls("a"), assistant, results("a")}, false},
		{"empty message interrupts batch", []model.Message{calls("a"), model.NewMessage(model.RoleTool), results("a")}, false},
		{"text tool message interrupts batch", []model.Message{calls("a"), model.NewTextMessage(model.RoleTool, "result"), results("a")}, false},
		{"user tool call", []model.Message{withRole(calls("a"), model.RoleUser), results("a")}, false},
		{"tool role tool call", []model.Message{withRole(calls("a"), model.RoleTool), results("a")}, false},
		{"system tool call", []model.Message{withRole(calls("a"), model.RoleSystem), results("a")}, false},
		{"user tool result", []model.Message{calls("a"), withRole(results("a"), model.RoleUser)}, false},
		{"assistant tool result", []model.Message{calls("a"), withRole(results("a"), model.RoleAssistant)}, false},
		{"system tool result", []model.Message{calls("a"), withRole(results("a"), model.RoleSystem)}, false},
		{"calls and results in one message", []model.Message{mixed}, false},
		{"result and next batch in one message", []model.Message{calls("a"), resultThenCall, results("b")}, false},
		{"mismatched whitespace ID", []model.Message{calls(" a"), results("a")}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := model.CloneMessages(tt.messages)
			if got := compactionPrefixSafe(tt.messages); got != tt.want {
				t.Errorf("compactionPrefixSafe() = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.messages, before) {
				t.Fatal("compactionPrefixSafe mutated the messages")
			}
		})
	}
}

func TestContextUsageProjectionMatchesFullHistoryAtEveryCut(t *testing.T) {
	message := func(role model.Role, parts ...model.Part) *model.Message {
		value := model.NewMessage(role, parts...)
		return &value
	}
	events := []*session.Event{
		{ID: "user", Type: session.EventTypeUser, Message: message(model.RoleUser, model.NewTextPart("中文 input"), model.NewMediaPart(model.MediaModalityImage, model.MediaSource{URI: "fixture://image"}, "image/png", "image"))},
		{ID: "call", Type: session.EventTypeToolCall, MessageID: "response", Message: message(model.RoleAssistant, model.NewToolUsePart("call", "read", json.RawMessage(`{"limit":20}`))), Invocation: &session.EventInvocation{Provider: "fixture", Model: "one", PromptPrefixTokens: 70}, Meta: map[string]any{"prompt_tokens": 90, "total_tokens": 100, "completion_tokens": 10}},
		{ID: "result", Type: session.EventTypeToolResult, Message: message(model.RoleTool, model.NewToolResultJSONPart("call", "read", map[string]any{"text": strings.Repeat("payload 中", 500)}, false))},
		{ID: "other", Type: session.EventTypeAssistant, Message: message(model.RoleAssistant, model.NewTextPart("other model output")), Invocation: &session.EventInvocation{Provider: "fixture", Model: "two"}, Meta: map[string]any{"prompt_tokens": 300, "total_tokens": 320, "completion_tokens": 20}},
		session.NewModelInvocationReceipt(model.Invocation{ID: "receipt", Provider: "fixture", Model: "one", Usage: model.Usage{PromptTokens: 7000, CompletionTokens: 30, TotalTokens: 7030}}, "main"),
		{ID: "compact", Type: session.EventTypeCompact, Text: "checkpoint replacement", Meta: map[string]any{session.CheckpointMetadataKey: session.CheckpointDataValue(session.CheckpointData{ContractVersion: 1, SummarizedThroughSeq: 2})}},
		{ID: "next-compact", Type: session.EventTypeCompact, Text: "new checkpoint", Meta: map[string]any{session.CheckpointMetadataKey: session.CheckpointDataValue(session.CheckpointData{ContractVersion: 1, SummarizedThroughSeq: 4})}},
		{ID: "next-user", Type: session.EventTypeUser, Message: message(model.RoleUser, model.NewTextPart("new input"))},
	}
	var projection ContextUsageProjection
	for index, event := range events {
		event.Seq = uint64(index + 1)
		projection.Append([]*session.Event{event})
		projection.Append(events[:index+1]) // A repeated catch-up must not recount.
		var canonical []*session.Event
		for _, original := range events[:index+1] {
			if session.IsCanonicalHistoryEvent(original) {
				canonical = append(canonical, original)
			}
		}
		for _, name := range []string{"one", "two", "unobserved"} {
			cfg := CompactionConfig{EstimatedPromptPrefixTokens: 11}
			want := computeUsageSnapshotForModel(canonical, 128000, cfg, "fixture", name, estimatePromptEventTokens)
			if got := projection.Snapshot(128000, cfg, "fixture", name); !reflect.DeepEqual(got, want) {
				t.Fatalf("cut=%d model=%s projected=%+v full=%+v", event.Seq, name, got, want)
			}
		}
	}
	for _, event := range projection.events {
		if event.Message != nil || event.Tool != nil {
			t.Fatal("context projection retained transcript payload")
		}
	}
	before := projection.Snapshot(128000, CompactionConfig{}, "fixture", "one")
	cloned := projection.Clone()
	cloned.Append([]*session.Event{{ID: "later", Seq: 9, Type: session.EventTypeUser, Text: "later context"}})
	if got := projection.Snapshot(128000, CompactionConfig{}, "fixture", "one"); got != before {
		t.Fatal("published projection changed while a clone caught up")
	}
}
