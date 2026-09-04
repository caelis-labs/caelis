package modelcatalog

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func TestLookupModelCapabilitiesFallsBackToBuiltinWhenDynamicCatalogUnavailable(t *testing.T) {
	dynamicMu.Lock()
	savedRemote := remoteCatalog
	savedEmbedded := embeddedCatalog
	savedLocal := localOverrides
	remoteCatalog = nil
	embeddedCatalog = nil
	localOverrides = nil
	dynamicMu.Unlock()
	defer func() {
		dynamicMu.Lock()
		remoteCatalog = savedRemote
		embeddedCatalog = savedEmbedded
		localOverrides = savedLocal
		dynamicMu.Unlock()
	}()

	caps, ok := LookupModelCapabilities("openai", "gpt-4o")
	if !ok {
		t.Fatal("LookupModelCapabilities(openai, gpt-4o) = false, want builtin fallback")
	}
	if caps.ContextWindowTokens <= 0 || caps.DefaultMaxOutputTokens <= 0 {
		t.Fatalf("caps = %#v, want populated builtin fallback", caps)
	}
}

func TestLookupSuggestedModelCapabilitiesDoesNotInheritVendorForCompatibleEndpoints(t *testing.T) {
	for _, test := range []struct {
		provider string
		model    string
	}{
		{provider: "openai-compatible", model: "gpt-4o-mini"},
		{provider: "anthropic-compatible", model: "claude-sonnet-4"},
	} {
		if caps, ok := LookupSuggestedModelCapabilities(test.provider, test.model); ok {
			t.Fatalf("LookupSuggestedModelCapabilities(%q, %q) = %#v, true; want no vendor inheritance", test.provider, test.model, caps)
		}
	}
}

func TestCompareReasoningEffortUsesCanonicalOrder(t *testing.T) {
	ordered := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "provider-specific"}
	for i := 1; i < len(ordered); i++ {
		if got := CompareReasoningEffort(ordered[i-1], ordered[i]); got >= 0 {
			t.Fatalf("CompareReasoningEffort(%q, %q) = %d, want less than zero", ordered[i-1], ordered[i], got)
		}
	}
	if got := CompareReasoningEffort("very-high", "xhigh"); got != 0 {
		t.Fatalf("CompareReasoningEffort(very-high, xhigh) = %d, want zero", got)
	}
}

func TestPreferredReasoningEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		levels []string
		want   string
	}{
		{name: "reasoning beats none", levels: []string{"none", "high"}, want: "high"},
		{name: "medium is preferred", levels: []string{"xhigh", "none", "low", "medium", "high"}, want: "medium"},
		{name: "lower middle without medium", levels: []string{"max", "low", "high", "xhigh"}, want: "high"},
		{name: "none only", levels: []string{"none"}, want: "none"},
		{name: "empty", levels: nil, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := PreferredReasoningEffort(tt.levels); got != tt.want {
				t.Fatalf("PreferredReasoningEffort(%#v) = %q, want %q", tt.levels, got, tt.want)
			}
		})
	}
}

func TestListCatalogModelsIncludesBuiltinDefaults(t *testing.T) {
	models := ListCatalogModels("deepseek")
	if len(models) == 0 {
		t.Fatal("ListCatalogModels(deepseek) returned no models")
	}
	foundFlash := false
	foundVision := false
	foundPro := false
	for _, model := range models {
		switch model {
		case "deepseek-v4-flash":
			foundFlash = true
		case "deepseek-v4-flash-vision-exp":
			foundVision = true
		case "deepseek-v4-pro":
			foundPro = true
		}
	}
	if !foundFlash || !foundVision || !foundPro {
		t.Fatalf("ListCatalogModels(deepseek) = %#v, want current Flash, Flash Vision, and Pro models", models)
	}
	for _, model := range models {
		if model == "deepseek-chat" || model == "deepseek-reasoner" {
			t.Fatalf("ListCatalogModels(deepseek) = %#v, did not want legacy DeepSeek models", models)
		}
	}
}

func TestCurrentGeminiStaticModels(t *testing.T) {
	disableDynamicCatalogForTest(t)

	wantModels := []string{
		"gemini-3.1-pro-preview",
		"gemini-3.1-pro-preview-customtools",
		"gemini-3.5-flash-lite",
		"gemini-3.6-flash",
		"gemini-3.7-flash",
		"gemini-3.8-flash",
	}
	models := ListCatalogModels("gemini")
	if !sameStrings(models, wantModels) {
		t.Fatalf("ListCatalogModels(gemini) = %#v, want current models %#v", models, wantModels)
	}
	for _, model := range wantModels {
		caps, ok := LookupModelCapabilities("gemini", model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(gemini, %q) = false, want true", model)
		}
		if caps.ContextWindowTokens != 1048576 || caps.MaxOutputTokens != 65536 {
			t.Fatalf("LookupModelCapabilities(gemini, %q) limits = %d/%d, want 1048576/65536",
				model, caps.ContextWindowTokens, caps.MaxOutputTokens)
		}
		if !caps.SupportsReasoning || caps.ReasoningMode != ReasoningModeEffort {
			t.Fatalf("LookupModelCapabilities(gemini, %q) reasoning = %#v, want effort reasoning", model, caps)
		}
		if !caps.SupportsToolCalls || !caps.SupportsImages || !caps.SupportsJSONOutput {
			t.Fatalf("LookupModelCapabilities(gemini, %q) caps = %#v, want tools/images/json", model, caps)
		}
	}

	for _, model := range []string{"gemini-3.8-flash", "gemini-3.7-flash"} {
		if levels := ReasoningLevelsForModel("gemini", model); !sameStrings(levels, []string{"low", "medium", "high"}) {
			t.Fatalf("ReasoningLevelsForModel(gemini, %q) = %#v, want low/medium/high", model, levels)
		}
	}
	for _, model := range []string{"gemini-3.6-flash", "gemini-3.5-flash-lite"} {
		if levels := ReasoningLevelsForModel("gemini", model); !sameStrings(levels, []string{"minimal", "low", "medium", "high"}) {
			t.Fatalf("ReasoningLevelsForModel(gemini, %q) = %#v, want minimal/low/medium/high", model, levels)
		}
	}
	if got := DefaultReasoningEffortForModel("gemini", "gemini-3.5-flash-lite"); got != "minimal" {
		t.Fatalf("DefaultReasoningEffortForModel(gemini, gemini-3.5-flash-lite) = %q, want minimal", got)
	}
	for _, legacy := range []string{"gemini-3.5-flash", "gemini-3-flash-preview", "gemini-3.1-flash-lite", "gemini-2.5-pro", "gemini-2.0-flash"} {
		if containsString(models, legacy) {
			t.Fatalf("ListCatalogModels(gemini) = %#v, did not want superseded %q", models, legacy)
		}
		if _, ok := LookupModelCapabilities("gemini", legacy); !ok {
			t.Fatalf("LookupModelCapabilities(gemini, %q) = false, want retained compatibility metadata", legacy)
		}
	}
}

func TestCurrentOpenAIStaticModels(t *testing.T) {
	disableDynamicCatalogForTest(t)

	wantModels := []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra"}
	models := ListCatalogModels("openai")
	if !sameStrings(models, wantModels) {
		t.Fatalf("ListCatalogModels(openai) = %#v, want current models %#v", models, wantModels)
	}
	for _, model := range wantModels {
		caps, ok := LookupModelCapabilities("openai", model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(openai, %q) = false, want true", model)
		}
		if caps.ContextWindowTokens != 1050000 || caps.MaxOutputTokens != 128000 || caps.DefaultMaxOutputTokens != 32768 {
			t.Fatalf("LookupModelCapabilities(openai, %q) limits = %d/%d default %d, want 1050000/128000 default 32768",
				model, caps.ContextWindowTokens, caps.MaxOutputTokens, caps.DefaultMaxOutputTokens)
		}
		if !caps.SupportsReasoning || caps.ReasoningMode != ReasoningModeEffort ||
			!caps.SupportsToolCalls || !caps.SupportsImages || !caps.SupportsJSONOutput {
			t.Fatalf("LookupModelCapabilities(openai, %q) caps = %#v, want reasoning/tools/images/json", model, caps)
		}
	}
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if levels := ReasoningLevelsForModel("openai", model); !sameStrings(levels, []string{"none", "low", "medium", "high", "xhigh", "max"}) {
			t.Fatalf("ReasoningLevelsForModel(openai, %q) = %#v, want none through max", model, levels)
		}
	}
	if levels := ReasoningLevelsForModel("openai", "gpt-6-astra"); !sameStrings(levels, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("ReasoningLevelsForModel(openai, gpt-6-astra) = %#v, want low through max", levels)
	}
	for _, legacy := range []string{"gpt-5.5", "gpt-5.4", "gpt-4o", "o1", "o3", "gpt-4.1-nano"} {
		if containsString(models, legacy) {
			t.Fatalf("ListCatalogModels(openai) = %#v, did not want superseded %q", models, legacy)
		}
		if _, ok := LookupModelCapabilities("openai", legacy); !ok {
			t.Fatalf("LookupModelCapabilities(openai, %q) = false, want retained compatibility metadata", legacy)
		}
	}
}

func TestCurrentAnthropicStaticModels(t *testing.T) {
	disableDynamicCatalogForTest(t)

	wantModels := []string{
		"claude-fable-5-1",
		"claude-haiku-4-5",
		"claude-mythos-5-1",
		"claude-opus-5",
		"claude-sonnet-5",
	}
	models := ListCatalogModels("anthropic")
	if !sameStrings(models, wantModels) {
		t.Fatalf("ListCatalogModels(anthropic) = %#v, want current models %#v", models, wantModels)
	}
	for _, model := range []string{"claude-fable-5-1", "claude-mythos-5-1", "claude-opus-5", "claude-sonnet-5"} {
		caps, ok := LookupModelCapabilities("anthropic", model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(anthropic, %q) = false, want true", model)
		}
		if caps.ContextWindowTokens != 1000000 || caps.MaxOutputTokens != 128000 || caps.DefaultMaxOutputTokens != 32768 {
			t.Fatalf("LookupModelCapabilities(anthropic, %q) limits = %d/%d default %d", model, caps.ContextWindowTokens, caps.MaxOutputTokens, caps.DefaultMaxOutputTokens)
		}
		if !caps.SupportsReasoning || caps.ReasoningMode != ReasoningModeEffort ||
			!sameStrings(caps.ReasoningEfforts, []string{"low", "medium", "high", "xhigh", "max"}) ||
			caps.DefaultReasoningEffort != "high" {
			t.Fatalf("LookupModelCapabilities(anthropic, %q) reasoning = %#v", model, caps)
		}
		if !caps.SupportsToolCalls || !caps.SupportsImages || !caps.SupportsJSONOutput {
			t.Fatalf("LookupModelCapabilities(anthropic, %q) caps = %#v, want tools/images/json", model, caps)
		}
	}
	haiku, ok := LookupModelCapabilities("anthropic", "claude-haiku-4-5-20251001")
	if !ok || haiku.ContextWindowTokens != 200000 || haiku.MaxOutputTokens != 64000 ||
		haiku.ReasoningMode != ReasoningModeToggle || len(haiku.ReasoningEfforts) != 0 {
		t.Fatalf("Claude Haiku 4.5 capabilities = %#v, want extended-thinking toggle without effort", haiku)
	}
	if levels := ReasoningLevelsForModel("anthropic", "claude-haiku-4-5-20251001"); !sameStrings(levels, []string{"none", "high"}) {
		t.Fatalf("ReasoningLevelsForModel(anthropic, Claude Haiku 4.5) = %#v, want none/high toggle", levels)
	}
	for _, legacy := range []string{"claude-fable-5", "claude-mythos-5", "claude-opus-4-8", "claude-sonnet-4-6", "claude-3-7-sonnet"} {
		if containsString(models, legacy) {
			t.Fatalf("ListCatalogModels(anthropic) = %#v, did not want superseded %q", models, legacy)
		}
		if _, ok := LookupModelCapabilities("anthropic", legacy); !ok {
			t.Fatalf("LookupModelCapabilities(anthropic, %q) = false, want retained compatibility metadata", legacy)
		}
	}
	for _, legacy := range []string{"claude-fable-5", "claude-mythos-5", "claude-opus-4-8"} {
		caps, _ := LookupModelCapabilities("anthropic", legacy)
		if !sameStrings(caps.ReasoningEfforts, []string{"low", "medium", "high", "xhigh", "max"}) || caps.DefaultReasoningEffort != "high" {
			t.Fatalf("LookupModelCapabilities(anthropic, %q) reasoning = %#v, want retained current effort metadata", legacy, caps)
		}
	}
	legacySonnet, _ := LookupModelCapabilities("anthropic", "claude-sonnet-4-6")
	if !sameStrings(legacySonnet.ReasoningEfforts, []string{"low", "medium", "high", "max"}) || legacySonnet.DefaultReasoningEffort != "high" {
		t.Fatalf("LookupModelCapabilities(anthropic, claude-sonnet-4-6) reasoning = %#v, want low through max without xhigh", legacySonnet)
	}
}

func TestCurrentMiniMaxAndVolcengineStaticModels(t *testing.T) {
	disableDynamicCatalogForTest(t)

	if models := ListCatalogModels("minimax"); !sameStrings(models, []string{"MiniMax-M3"}) {
		t.Fatalf("ListCatalogModels(minimax) = %#v, want only current MiniMax-M3", models)
	}
	minimaxCaps, ok := LookupModelCapabilities("minimax", "MiniMax-M3")
	if !ok {
		t.Fatal("LookupModelCapabilities(minimax, MiniMax-M3) = false, want true")
	}
	if minimaxCaps.ContextWindowTokens != 1000000 || minimaxCaps.MaxOutputTokens != 524288 ||
		minimaxCaps.ReasoningMode != ReasoningModeToggle || !minimaxCaps.SupportsToolCalls || !minimaxCaps.SupportsImages {
		t.Fatalf("MiniMax-M3 caps = %#v, want current limits and toggle reasoning/tools/images", minimaxCaps)
	}
	legacyMiniMax, ok := LookupModelCapabilities("minimax", "MiniMax-M2.7")
	if !ok || legacyMiniMax.MaxOutputTokens != 204800 || legacyMiniMax.SupportsImages {
		t.Fatalf("MiniMax-M2.7 retained caps = %#v, want 204800 output without image input", legacyMiniMax)
	}

	wantVolcengine := []string{
		"deepseek-v4-flash",
		"deepseek-v4-pro",
		"doubao-seed-2.0-lite",
		"doubao-seed-2.1-turbo",
		"doubao-seed-evolving",
		"glm-5.3",
		"glm-5.3-flash",
		"kimi-k2.7-code",
		"minimax-m3",
	}
	models := ListCatalogModels("volcengine")
	if !sameStrings(models, wantVolcengine) {
		t.Fatalf("ListCatalogModels(volcengine) = %#v, want current models %#v", models, wantVolcengine)
	}
	for _, test := range []struct {
		model     string
		context   int
		maxOutput int
		images    bool
	}{
		{model: "doubao-seed-evolving", context: 1048576, maxOutput: 262144, images: true},
		{model: "doubao-seed-2.1-turbo", context: 262144, maxOutput: 262144, images: true},
		{model: "deepseek-v4-pro", context: 1048576, maxOutput: 393216},
		{model: "glm-5.3-flash", context: 1048576, maxOutput: 131072, images: true},
		{model: "minimax-m3", context: 1048576, maxOutput: 131072, images: true},
		{model: "kimi-k2.7-code", context: 262144, maxOutput: 32768},
	} {
		caps, ok := LookupModelCapabilities("volcengine", test.model)
		if !ok || caps.ContextWindowTokens != test.context || caps.MaxOutputTokens != test.maxOutput ||
			caps.SupportsImages != test.images || !caps.SupportsReasoning || !caps.SupportsToolCalls || !caps.SupportsJSONOutput {
			t.Fatalf("LookupModelCapabilities(volcengine, %q) = %#v, want current maintained capabilities", test.model, caps)
		}
	}
	for _, legacy := range []string{"doubao-seed-1.8", "doubao-seed-2.0-mini", "doubao-seed-code", "glm-4.7", "deepseek-v3.2", "kimi-k2.5", "kimi-k3", "minimax-m2.5"} {
		if containsString(models, legacy) {
			t.Fatalf("ListCatalogModels(volcengine) = %#v, did not want superseded or non-Coding-Plan %q", models, legacy)
		}
		if _, ok := LookupModelCapabilities("volcengine", legacy); !ok {
			t.Fatalf("LookupModelCapabilities(volcengine, %q) = false, want retained compatibility metadata", legacy)
		}
	}
	glm47, _ := LookupModelCapabilities("volcengine", "glm-4.7")
	if glm47.ContextWindowTokens != 200000 || glm47.MaxOutputTokens != 131072 {
		t.Fatalf("LookupModelCapabilities(volcengine, glm-4.7) limits = %d/%d, want 200000/131072", glm47.ContextWindowTokens, glm47.MaxOutputTokens)
	}
	if _, ok := LookupModelCapabilities("volcengine", "doubao-seed-2.1-pro"); ok {
		t.Fatal("LookupModelCapabilities(volcengine, doubao-seed-2.1-pro) = true, want invalid Coding Plan short ID removed")
	}
}

func TestOllamaCatalogMaintainsCloudModelsOnly(t *testing.T) {
	disableDynamicCatalogForTest(t)

	if models := ListCatalogModels("ollama"); len(models) != 0 {
		t.Fatalf("ListCatalogModels(ollama) = %#v, want no maintained local models", models)
	}

	wantModels := []string{"deepseek-v4-flash", "deepseek-v4-pro", "glm-5.3", "glm-5.3-flash", "kimi-k2.7-code", "kimi-k3", "minimax-m3"}
	models := ListCatalogModels(OllamaCloudProvider)
	if !sameStrings(models, wantModels) {
		t.Fatalf("ListCatalogModels(%s) = %#v, want %#v", OllamaCloudProvider, models, wantModels)
	}
	wantPriorityOrder := []string{"glm-5.3", "glm-5.3-flash", "minimax-m3", "kimi-k3", "kimi-k2.7-code", "deepseek-v4-flash", "deepseek-v4-pro"}
	if got := ListOllamaCloudModels(); !sameStrings(got, wantPriorityOrder) {
		t.Fatalf("ListOllamaCloudModels() = %#v, want priority order %#v", got, wantPriorityOrder)
	}
	if containsString(models, "glm-5.2") {
		t.Fatalf("ListCatalogModels(%s) = %#v, did not want superseded glm-5.2", OllamaCloudProvider, models)
	}
	if _, ok := LookupModelCapabilities(OllamaCloudProvider, "glm-5.2"); !ok {
		t.Fatal("LookupModelCapabilities(ollama-cloud, glm-5.2) = false, want retained compatibility metadata")
	}
	wantContexts := map[string]int{
		"glm-5.3":           1000000,
		"glm-5.3-flash":     1000000,
		"minimax-m3":        524288,
		"kimi-k3":           1000000,
		"kimi-k2.7-code":    262144,
		"deepseek-v4-flash": 1000000,
		"deepseek-v4-pro":   1000000,
	}
	wantImages := map[string]bool{
		"glm-5.3-flash":  true,
		"minimax-m3":     true,
		"kimi-k3":        true,
		"kimi-k2.7-code": true,
	}
	for _, model := range wantModels {
		caps, ok := LookupModelCapabilities(OllamaCloudProvider, model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(%s, %q) = false, want true", OllamaCloudProvider, model)
		}
		if caps.ContextWindowTokens != wantContexts[model] || caps.MaxOutputTokens != 0 || caps.DefaultMaxOutputTokens != 32768 {
			t.Fatalf("LookupModelCapabilities(%s, %q) limits = %d/%d default %d",
				OllamaCloudProvider, model, caps.ContextWindowTokens, caps.MaxOutputTokens, caps.DefaultMaxOutputTokens)
		}
		if !caps.SupportsReasoning || !caps.SupportsToolCalls || !caps.SupportsJSONOutput {
			t.Fatalf("LookupModelCapabilities(%s, %q) caps = %#v, want reasoning/tools/json", OllamaCloudProvider, model, caps)
		}
		if caps.SupportsImages != wantImages[model] {
			t.Fatalf("LookupModelCapabilities(%s, %q).SupportsImages = %v, want %v",
				OllamaCloudProvider, model, caps.SupportsImages, wantImages[model])
		}
	}

	for _, model := range []string{"glm-5.3", "glm-5.3-flash"} {
		if levels := ReasoningLevelsForModel(OllamaCloudProvider, model); !sameStrings(levels, []string{"low", "high", "max"}) {
			t.Fatalf("ReasoningLevelsForModel(%s, %q) = %#v, want low/high/max", OllamaCloudProvider, model, levels)
		}
		if got := DefaultReasoningEffortForModel(OllamaCloudProvider, model); got != "max" {
			t.Fatalf("DefaultReasoningEffortForModel(%s, %q) = %q, want max", OllamaCloudProvider, model, got)
		}
	}
	for _, model := range []string{"minimax-m3", "kimi-k3", "kimi-k2.7-code"} {
		caps, _ := LookupModelCapabilities(OllamaCloudProvider, model)
		if caps.ReasoningMode != ReasoningModeToggle {
			t.Fatalf("LookupModelCapabilities(%s, %q).ReasoningMode = %q, want toggle", OllamaCloudProvider, model, caps.ReasoningMode)
		}
		if levels := ReasoningLevelsForModel(OllamaCloudProvider, model); !sameStrings(levels, []string{"none", "high"}) {
			t.Fatalf("ReasoningLevelsForModel(%s, %q) = %#v, want none/high", OllamaCloudProvider, model, levels)
		}
	}
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		if levels := ReasoningLevelsForModel(OllamaCloudProvider, model); !sameStrings(levels, []string{"none", "high", "max"}) {
			t.Fatalf("ReasoningLevelsForModel(%s, %q) = %#v, want none/high/max", OllamaCloudProvider, model, levels)
		}
	}
}

func TestGrok45StaticCapabilitiesIncludeImageInput(t *testing.T) {
	caps, ok := LookupModelCapabilities("xai", "grok-4.5")
	if !ok {
		t.Fatal("LookupModelCapabilities(xai, grok-4.5) = false, want true")
	}
	if caps.ContextWindowTokens != 500000 || caps.MaxOutputTokens != 0 || caps.DefaultMaxOutputTokens != 32768 {
		t.Fatalf("LookupModelCapabilities(xai, grok-4.5) limits = %#v", caps)
	}
	if !caps.SupportsImages || !caps.SupportsToolCalls || !caps.SupportsReasoning {
		t.Fatalf("LookupModelCapabilities(xai, grok-4.5) capabilities = %#v", caps)
	}
	if !caps.SupportsJSONOutput || caps.ReasoningMode != ReasoningModeEffort ||
		!sameStrings(caps.ReasoningEfforts, []string{"low", "medium", "high"}) ||
		caps.DefaultReasoningEffort != "high" {
		t.Fatalf("LookupModelCapabilities(xai, grok-4.5) reasoning/output = %#v", caps)
	}
}

func TestGrokCatalogRecommendsOnlyCurrentModel(t *testing.T) {
	disableDynamicCatalogForTest(t)

	if models := ListCatalogModels("xai"); !sameStrings(models, []string{"grok-4.6"}) {
		t.Fatalf("ListCatalogModels(xai) = %#v, want only grok-4.6", models)
	}
	if _, ok := LookupModelCapabilities("xai", "grok-4.5"); !ok {
		t.Fatal("LookupModelCapabilities(xai, grok-4.5) = false, want retained compatibility metadata")
	}
}

func TestGrok46StaticCapabilitiesIncludeXHighReasoning(t *testing.T) {
	for _, model := range []string{"grok-4.6", "grok-4.6-build"} {
		caps, ok := LookupModelCapabilities("xai", model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(xai, %q) = false, want true", model)
		}
		if caps.ContextWindowTokens != 500000 || caps.MaxOutputTokens != 0 || caps.DefaultMaxOutputTokens != 32768 {
			t.Fatalf("LookupModelCapabilities(xai, %q) limits = %#v", model, caps)
		}
		if !caps.SupportsImages || !caps.SupportsToolCalls || !caps.SupportsReasoning {
			t.Fatalf("LookupModelCapabilities(xai, %q) capabilities = %#v", model, caps)
		}
		if !caps.SupportsJSONOutput || caps.ReasoningMode != ReasoningModeEffort ||
			!sameStrings(caps.ReasoningEfforts, []string{"low", "medium", "high", "xhigh"}) ||
			caps.DefaultReasoningEffort != "high" {
			t.Fatalf("LookupModelCapabilities(xai, %q) reasoning/output = %#v", model, caps)
		}
		if levels := ReasoningLevelsForModel("xai", model); !sameStrings(levels, []string{"low", "medium", "high", "xhigh"}) {
			t.Fatalf("ReasoningLevelsForModel(xai, %q) = %#v, want low/medium/high/xhigh", model, levels)
		}
	}
}

func TestDeepSeekStaticModelsExposeMaintainedCapabilities(t *testing.T) {
	tests := []struct {
		model  string
		images bool
	}{
		{model: "deepseek-v4-flash"},
		{model: "deepseek-v4-flash-vision-exp", images: true},
		{model: "deepseek-v4-pro"},
	}
	for _, test := range tests {
		caps, ok := LookupModelCapabilities("deepseek", test.model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(deepseek, %q) = false, want true", test.model)
		}
		if !caps.SupportsReasoning || caps.ReasoningMode != ReasoningModeToggle {
			t.Fatalf("LookupModelCapabilities(deepseek, %q) = %#v, want toggle reasoning", test.model, caps)
		}
		if caps.ContextWindowTokens != 1048576 || caps.MaxOutputTokens != 393216 || caps.DefaultMaxOutputTokens != 32768 {
			t.Fatalf("LookupModelCapabilities(deepseek, %q) limits = %d/%d default %d, want 1048576/393216 default 32768",
				test.model, caps.ContextWindowTokens, caps.MaxOutputTokens, caps.DefaultMaxOutputTokens)
		}
		if caps.SupportsImages != test.images || !caps.SupportsToolCalls || !caps.SupportsJSONOutput {
			t.Fatalf("LookupModelCapabilities(deepseek, %q) capabilities = %#v, want images=%v, tools, and JSON", test.model, caps, test.images)
		}
		if !sameStrings(caps.ReasoningEfforts, []string{"low", "high", "max"}) {
			t.Fatalf("LookupModelCapabilities(deepseek, %q) efforts = %#v, want low/high/max", test.model, caps.ReasoningEfforts)
		}
		if levels := ReasoningLevelsForModel("deepseek", test.model); !sameStrings(levels, []string{"none", "low", "high", "max"}) {
			t.Fatalf("ReasoningLevelsForModel(deepseek, %q) = %#v, want none/low/high/max", test.model, levels)
		}
	}
}

func TestMimoStaticModelsMatchBuiltInCatalog(t *testing.T) {
	models := ListCatalogModels("xiaomi")
	wantModels := []string{"mimo-v2.5-pro", "mimo-v2.5"}
	for _, model := range wantModels {
		if !containsString(models, model) {
			t.Fatalf("ListCatalogModels(xiaomi) = %#v, missing %q", models, model)
		}
	}
	for _, model := range []string{"mimo-v2-pro", "mimo-v2-omni", "mimo-v2-flash", "mimo-v2-reasoner", "MiMo-VL-7B-RL"} {
		if containsString(models, model) {
			t.Fatalf("ListCatalogModels(xiaomi) = %#v, did not want stale model %q", models, model)
		}
	}

	tests := []struct {
		model       string
		context     int
		maxOutput   int
		imageInputs bool
	}{
		{model: "mimo-v2.5-pro", context: 1048576, maxOutput: 131072},
		{model: "mimo-v2.5", context: 1048576, maxOutput: 131072, imageInputs: true},
	}
	for _, tc := range tests {
		caps, ok := LookupModelCapabilities("xiaomi", tc.model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(xiaomi, %q) = false, want true", tc.model)
		}
		if caps.ContextWindowTokens != tc.context || caps.MaxOutputTokens != tc.maxOutput {
			t.Fatalf("LookupModelCapabilities(xiaomi, %q) limits = %d/%d, want %d/%d",
				tc.model, caps.ContextWindowTokens, caps.MaxOutputTokens, tc.context, tc.maxOutput)
		}
		if caps.SupportsImages != tc.imageInputs {
			t.Fatalf("LookupModelCapabilities(xiaomi, %q).SupportsImages = %v, want %v", tc.model, caps.SupportsImages, tc.imageInputs)
		}
		if !caps.SupportsReasoning || caps.ReasoningMode != ReasoningModeToggle || !caps.SupportsToolCalls || !caps.SupportsJSONOutput {
			t.Fatalf("LookupModelCapabilities(xiaomi, %q) caps = %#v, want toggle reasoning/tools/json", tc.model, caps)
		}
	}
}

func TestListCatalogModelsUsesStaticCatalogOnly(t *testing.T) {
	dynamicMu.Lock()
	savedRemote := remoteCatalog
	savedEmbedded := embeddedCatalog
	savedLocal := localOverrides
	remoteCatalog = capSnapshot{
		"openai:gpt-from-remote": {
			ContextWindow: 1000,
			MaxOutput:     100,
		},
		"deepseek:remote-deepseek-model": {
			ContextWindow: 1000,
			MaxOutput:     100,
		},
	}
	embeddedCatalog = capSnapshot{
		"openai:gpt-from-embedded": {
			ContextWindow: 1000,
			MaxOutput:     100,
		},
		"minimax:remote-minimax-model": {
			ContextWindow: 1000,
			MaxOutput:     100,
		},
	}
	localOverrides = nil
	dynamicMu.Unlock()
	defer func() {
		dynamicMu.Lock()
		remoteCatalog = savedRemote
		embeddedCatalog = savedEmbedded
		localOverrides = savedLocal
		dynamicMu.Unlock()
	}()

	for _, provider := range []string{"openai", "deepseek", "minimax"} {
		models := ListCatalogModels(provider)
		for _, model := range models {
			switch model {
			case "gpt-from-remote", "gpt-from-embedded", "remote-deepseek-model", "remote-minimax-model":
				t.Fatalf("ListCatalogModels(%q) = %#v, did not want remote/snapshot model %q", provider, models, model)
			}
		}
	}
}

func TestListRecommendedModelsAllowsLocalOverrideToReAddHiddenBuiltin(t *testing.T) {
	dynamicMu.Lock()
	savedLocal := localOverrides
	localOverrides = capSnapshot{
		"openai:gpt-5.5": {
			ContextWindow: 1048576,
			MaxOutput:     128000,
		},
	}
	dynamicMu.Unlock()
	defer func() {
		dynamicMu.Lock()
		localOverrides = savedLocal
		dynamicMu.Unlock()
	}()

	if models := ListRecommendedModels("openai"); !containsString(models, "gpt-5.5") {
		t.Fatalf("ListRecommendedModels(openai) = %#v, want explicit local override to re-add hidden gpt-5.5", models)
	}
}

func TestListModelDirectoryModelsUsesDynamicCatalog(t *testing.T) {
	dynamicMu.Lock()
	savedRemote := remoteCatalog
	savedEmbedded := embeddedCatalog
	savedLocal := localOverrides
	remoteCatalog = capSnapshot{
		"openai:gpt-from-remote": {
			ContextWindow: 1000,
			MaxOutput:     100,
		},
		"anthropic:claude-from-remote": {
			ContextWindow: 1000,
			MaxOutput:     100,
		},
		"openrouter:openai/gpt-from-openrouter": {
			ContextWindow: 1000,
			MaxOutput:     100,
		},
		"google:gemini-from-google": {
			ContextWindow: 1000,
			MaxOutput:     100,
		},
		"ai:accidental-substring-match": {
			ContextWindow: 1000,
			MaxOutput:     100,
		},
	}
	embeddedCatalog = capSnapshot{
		"openai:gpt-from-embedded": {
			ContextWindow: 1000,
			MaxOutput:     100,
		},
	}
	localOverrides = capSnapshot{
		"openai:gpt-from-local": {
			ContextWindow: 1000,
			MaxOutput:     100,
		},
	}
	dynamicMu.Unlock()
	defer func() {
		dynamicMu.Lock()
		remoteCatalog = savedRemote
		embeddedCatalog = savedEmbedded
		localOverrides = savedLocal
		dynamicMu.Unlock()
	}()

	if models := ListModelDirectoryModels("openai-compatible"); len(models) != 0 {
		t.Fatalf("ListModelDirectoryModels(openai-compatible) = %#v, want no assumed upstream directory", models)
	}
	for _, stale := range []string{"gpt-from-remote", "gpt-from-embedded"} {
		if containsString(ListCatalogModels("openai-compatible"), stale) {
			t.Fatalf("ListCatalogModels(openai-compatible) included dynamic model %q", stale)
		}
	}

	if models := ListModelDirectoryModels("anthropic-compatible"); len(models) != 0 {
		t.Fatalf("ListModelDirectoryModels(anthropic-compatible) = %#v, want no assumed upstream directory", models)
	}
	openRouterModels := ListModelDirectoryModels("openrouter")
	if !containsString(openRouterModels, "openai/gpt-from-openrouter") {
		t.Fatalf("ListModelDirectoryModels(openrouter) = %#v, missing openai/gpt-from-openrouter", openRouterModels)
	}
	geminiModels := ListModelDirectoryModels("gemini")
	if !containsString(geminiModels, "gemini-from-google") {
		t.Fatalf("ListModelDirectoryModels(gemini) = %#v, missing aliased google model", geminiModels)
	}
}

func TestLookupModelCapabilitiesPrefersBuiltinOverSnapshot(t *testing.T) {
	dynamicMu.Lock()
	savedRemote := remoteCatalog
	savedEmbedded := embeddedCatalog
	savedLocal := localOverrides
	remoteCatalog = capSnapshot{
		"openai:gpt-4o": {
			ContextWindow: 1,
			MaxOutput:     1,
		},
	}
	embeddedCatalog = nil
	localOverrides = nil
	dynamicMu.Unlock()
	defer func() {
		dynamicMu.Lock()
		remoteCatalog = savedRemote
		embeddedCatalog = savedEmbedded
		localOverrides = savedLocal
		dynamicMu.Unlock()
	}()

	caps, ok := LookupModelCapabilities("openai", "gpt-4o")
	if !ok {
		t.Fatal("LookupModelCapabilities(openai, gpt-4o) = false, want builtin")
	}
	if caps.ContextWindowTokens <= 1 || caps.MaxOutputTokens <= 1 {
		t.Fatalf("caps = %#v, want builtin values instead of snapshot values", caps)
	}
}

func TestLookupModelCapabilitiesUsesSnapshotForCustomModel(t *testing.T) {
	dynamicMu.Lock()
	savedRemote := remoteCatalog
	savedEmbedded := embeddedCatalog
	savedLocal := localOverrides
	remoteCatalog = nil
	embeddedCatalog = capSnapshot{
		"openai:custom-snapshot-model": {
			ContextWindow: 99000,
			MaxOutput:     9000,
		},
	}
	localOverrides = nil
	dynamicMu.Unlock()
	defer func() {
		dynamicMu.Lock()
		remoteCatalog = savedRemote
		embeddedCatalog = savedEmbedded
		localOverrides = savedLocal
		dynamicMu.Unlock()
	}()

	caps, ok := LookupModelCapabilities("openai", "custom-snapshot-model")
	if !ok {
		t.Fatal("LookupModelCapabilities(openai, custom-snapshot-model) = false, want snapshot fallback")
	}
	if caps.ContextWindowTokens != 99000 || caps.MaxOutputTokens != 9000 {
		t.Fatalf("caps = %#v, want snapshot fallback values", caps)
	}
}

func TestParseSnapshotBytesInvalidJSONGracefullyDegrades(t *testing.T) {
	if snap := parseSnapshotBytes([]byte("{not-json")); snap != nil {
		t.Fatalf("parseSnapshotBytes(invalid) = %#v, want nil", snap)
	}
}

func TestParseEmbeddedSnapshotBytesSupportsGzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(`{"openai:custom-gzip-model":{"context_window":1234,"max_output":567,"tool_calls":true,"json_output":true}}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	snap := parseEmbeddedSnapshotBytes(buf.Bytes())
	caps, ok := searchCapSnapshot(snap, "openai", "custom-gzip-model")
	if !ok {
		t.Fatal("searchCapSnapshot(openai, custom-gzip-model) = false, want true")
	}
	if caps.ContextWindowTokens != 1234 || caps.MaxOutputTokens != 567 || !caps.SupportsToolCalls || !caps.SupportsJSONOutput {
		t.Fatalf("caps = %#v, want gzip snapshot values", caps)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func disableDynamicCatalogForTest(t *testing.T) {
	t.Helper()
	dynamicMu.Lock()
	savedRemote := remoteCatalog
	savedEmbedded := embeddedCatalog
	savedLocal := localOverrides
	remoteCatalog = nil
	embeddedCatalog = nil
	localOverrides = nil
	dynamicMu.Unlock()
	t.Cleanup(func() {
		dynamicMu.Lock()
		remoteCatalog = savedRemote
		embeddedCatalog = savedEmbedded
		localOverrides = savedLocal
		dynamicMu.Unlock()
	})
}
