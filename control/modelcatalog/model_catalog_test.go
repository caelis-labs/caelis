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
		"gemini-3.5-flash",
		"gemini-3.1-pro-preview",
		"gemini-3.1-pro-preview-customtools",
		"gemini-3-flash-preview",
		"gemini-3.1-flash-lite",
	}
	models := ListCatalogModels("gemini")
	for _, model := range wantModels {
		if !containsString(models, model) {
			t.Fatalf("ListCatalogModels(gemini) = %#v, missing %q", models, model)
		}
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
		if !sameStrings(ReasoningLevelsForModel("gemini", model), []string{"low", "medium", "high"}) {
			t.Fatalf("ReasoningLevelsForModel(gemini, %q) = %#v, want low/medium/high",
				model, ReasoningLevelsForModel("gemini", model))
		}
		if !caps.SupportsToolCalls || !caps.SupportsImages || !caps.SupportsJSONOutput {
			t.Fatalf("LookupModelCapabilities(gemini, %q) caps = %#v, want tools/images/json", model, caps)
		}
	}
}

func TestCurrentOpenAIStaticModels(t *testing.T) {
	disableDynamicCatalogForTest(t)

	tests := []struct {
		model   string
		context int
	}{
		{model: "gpt-5.5", context: 1050000},
		{model: "gpt-5.5-pro", context: 1050000},
		{model: "gpt-5.5-instant", context: 400000},
		{model: "gpt-5.4", context: 1050000},
		{model: "gpt-5.4-pro", context: 1050000},
		{model: "gpt-5.4-mini", context: 400000},
		{model: "gpt-5.4-nano", context: 400000},
	}
	models := ListCatalogModels("openai")
	for _, tc := range tests {
		if !containsString(models, tc.model) {
			t.Fatalf("ListCatalogModels(openai) = %#v, missing %q", models, tc.model)
		}
		caps, ok := LookupModelCapabilities("openai", tc.model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(openai, %q) = false, want true", tc.model)
		}
		if caps.ContextWindowTokens != tc.context || caps.MaxOutputTokens != 128000 {
			t.Fatalf("LookupModelCapabilities(openai, %q) limits = %d/%d, want %d/128000",
				tc.model, caps.ContextWindowTokens, caps.MaxOutputTokens, tc.context)
		}
		if !caps.SupportsReasoning || caps.ReasoningMode != ReasoningModeEffort {
			t.Fatalf("LookupModelCapabilities(openai, %q) reasoning = %#v, want effort reasoning", tc.model, caps)
		}
		if !sameStrings(ReasoningLevelsForModel("openai", tc.model), []string{"none", "low", "medium", "high", "xhigh"}) {
			t.Fatalf("ReasoningLevelsForModel(openai, %q) = %#v, want none/low/medium/high/xhigh",
				tc.model, ReasoningLevelsForModel("openai", tc.model))
		}
		if got := DefaultReasoningEffortForModel("openai", tc.model); got != "medium" {
			t.Fatalf("DefaultReasoningEffortForModel(openai, %q) = %q, want medium", tc.model, got)
		}
		if !caps.SupportsToolCalls || !caps.SupportsImages || !caps.SupportsJSONOutput {
			t.Fatalf("LookupModelCapabilities(openai, %q) caps = %#v, want tools/images/json", tc.model, caps)
		}
	}
}

func TestCurrentAnthropicStaticModels(t *testing.T) {
	disableDynamicCatalogForTest(t)

	tests := []struct {
		model     string
		context   int
		maxOutput int
	}{
		{model: "claude-fable-5", context: 1000000, maxOutput: 128000},
		{model: "claude-mythos-5", context: 1000000, maxOutput: 128000},
		{model: "claude-opus-4-8", context: 1000000, maxOutput: 128000},
		{model: "claude-sonnet-4-6", context: 1000000, maxOutput: 64000},
		{model: "claude-haiku-4-5-20251001", context: 200000, maxOutput: 64000},
	}
	models := ListCatalogModels("anthropic")
	for _, tc := range tests {
		if tc.model != "claude-haiku-4-5-20251001" && !containsString(models, tc.model) {
			t.Fatalf("ListCatalogModels(anthropic) = %#v, missing %q", models, tc.model)
		}
		caps, ok := LookupModelCapabilities("anthropic", tc.model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(anthropic, %q) = false, want true", tc.model)
		}
		if caps.ContextWindowTokens != tc.context || caps.MaxOutputTokens != tc.maxOutput {
			t.Fatalf("LookupModelCapabilities(anthropic, %q) limits = %d/%d, want %d/%d",
				tc.model, caps.ContextWindowTokens, caps.MaxOutputTokens, tc.context, tc.maxOutput)
		}
		if !caps.SupportsReasoning || caps.ReasoningMode != ReasoningModeEffort {
			t.Fatalf("LookupModelCapabilities(anthropic, %q) reasoning = %#v, want effort reasoning", tc.model, caps)
		}
		if tc.model == "claude-opus-4-8" {
			if got := DefaultReasoningEffortForModel("anthropic", tc.model); got != "high" {
				t.Fatalf("DefaultReasoningEffortForModel(anthropic, %q) = %q, want high", tc.model, got)
			}
		}
		if !caps.SupportsToolCalls || !caps.SupportsImages || !caps.SupportsJSONOutput {
			t.Fatalf("LookupModelCapabilities(anthropic, %q) caps = %#v, want tools/images/json", tc.model, caps)
		}
	}
}

func TestCurrentMiniMaxAndVolcengineStaticModels(t *testing.T) {
	disableDynamicCatalogForTest(t)

	minimaxCaps, ok := LookupModelCapabilities("minimax", "MiniMax-M3")
	if !ok {
		t.Fatal("LookupModelCapabilities(minimax, MiniMax-M3) = false, want true")
	}
	if minimaxCaps.ContextWindowTokens != 1000000 || minimaxCaps.MaxOutputTokens != 1000000 {
		t.Fatalf("MiniMax-M3 limits = %d/%d, want 1000000/1000000",
			minimaxCaps.ContextWindowTokens, minimaxCaps.MaxOutputTokens)
	}
	if !minimaxCaps.SupportsReasoning || !minimaxCaps.SupportsToolCalls || !minimaxCaps.SupportsImages {
		t.Fatalf("MiniMax-M3 caps = %#v, want reasoning/tools/images", minimaxCaps)
	}

	for _, model := range []string{"doubao-seed-1.8", "doubao-seed-2.0-mini"} {
		caps, ok := LookupModelCapabilities("volcengine", model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(volcengine, %q) = false, want true", model)
		}
		if caps.ContextWindowTokens != 256000 || caps.MaxOutputTokens != 64000 {
			t.Fatalf("LookupModelCapabilities(volcengine, %q) limits = %d/%d, want 256000/64000",
				model, caps.ContextWindowTokens, caps.MaxOutputTokens)
		}
		if !caps.SupportsReasoning || !caps.SupportsToolCalls || !caps.SupportsImages {
			t.Fatalf("LookupModelCapabilities(volcengine, %q) caps = %#v, want reasoning/tools/images", model, caps)
		}
	}
}

func TestOllamaCatalogMaintainsCloudModelsOnly(t *testing.T) {
	disableDynamicCatalogForTest(t)

	if models := ListCatalogModels("ollama"); len(models) != 0 {
		t.Fatalf("ListCatalogModels(ollama) = %#v, want no maintained local models", models)
	}

	wantModels := []string{"deepseek-v4-flash", "deepseek-v4-pro", "glm-5.2", "kimi-k3", "minimax-m3"}
	models := ListCatalogModels(OllamaCloudProvider)
	if !sameStrings(models, wantModels) {
		t.Fatalf("ListCatalogModels(%s) = %#v, want %#v", OllamaCloudProvider, models, wantModels)
	}
	for _, retired := range []string{"kimi-k2.7-code", "nemotron-3-super"} {
		if containsString(models, retired) {
			t.Fatalf("ListCatalogModels(%s) = %#v, did not want retired %q", OllamaCloudProvider, models, retired)
		}
	}
	wantContexts := map[string]int{
		"glm-5.2":           1000000,
		"minimax-m3":        524288,
		"kimi-k3":           1000000,
		"deepseek-v4-flash": 1000000,
		"deepseek-v4-pro":   1000000,
	}
	wantImages := map[string]bool{
		"minimax-m3": true,
		"kimi-k3":    true,
	}
	for _, model := range wantModels {
		caps, ok := LookupModelCapabilities(OllamaCloudProvider, model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(%s, %q) = false, want true", OllamaCloudProvider, model)
		}
		if caps.ContextWindowTokens != wantContexts[model] ||
			caps.MaxOutputTokens != 0 ||
			caps.DefaultMaxOutputTokens != 32768 {
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

	if levels := ReasoningLevelsForModel(OllamaCloudProvider, "glm-5.2"); !sameStrings(levels, []string{"high", "max"}) {
		t.Fatalf("ReasoningLevelsForModel(%s, glm-5.2) = %#v, want high/max", OllamaCloudProvider, levels)
	}
	if got := DefaultReasoningEffortForModel(OllamaCloudProvider, "glm-5.2"); got != "high" {
		t.Fatalf("DefaultReasoningEffortForModel(%s, glm-5.2) = %q, want high", OllamaCloudProvider, got)
	}
	for _, model := range []string{"minimax-m3", "kimi-k3"} {
		caps, _ := LookupModelCapabilities(OllamaCloudProvider, model)
		if caps.ReasoningMode != ReasoningModeToggle {
			t.Fatalf("LookupModelCapabilities(%s, %q).ReasoningMode = %q, want toggle", OllamaCloudProvider, model, caps.ReasoningMode)
		}
		if levels := ReasoningLevelsForModel(OllamaCloudProvider, model); !sameStrings(levels, []string{"none", "high"}) {
			t.Fatalf("ReasoningLevelsForModel(%s, %q) = %#v, want none/high", OllamaCloudProvider, model, levels)
		}
	}
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		caps, _ := LookupModelCapabilities(OllamaCloudProvider, model)
		if caps.ReasoningMode != ReasoningModeToggle {
			t.Fatalf("LookupModelCapabilities(%s, %q).ReasoningMode = %q, want toggle", OllamaCloudProvider, model, caps.ReasoningMode)
		}
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
	if caps.ContextWindowTokens != 500000 || caps.MaxOutputTokens != 32768 || caps.DefaultMaxOutputTokens != 32768 {
		t.Fatalf("LookupModelCapabilities(xai, grok-4.5) limits = %#v", caps)
	}
	if !caps.SupportsImages || !caps.SupportsToolCalls || !caps.SupportsReasoning {
		t.Fatalf("LookupModelCapabilities(xai, grok-4.5) capabilities = %#v", caps)
	}
	if caps.SupportsJSONOutput || caps.ReasoningMode != ReasoningModeEffort ||
		!sameStrings(caps.ReasoningEfforts, []string{"low", "medium", "high"}) ||
		caps.DefaultReasoningEffort != "high" {
		t.Fatalf("LookupModelCapabilities(xai, grok-4.5) reasoning/output = %#v", caps)
	}
}

func TestGrok46StaticCapabilitiesIncludeXHighReasoning(t *testing.T) {
	for _, model := range []string{"grok-4.6", "grok-4.6-build"} {
		caps, ok := LookupModelCapabilities("xai", model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(xai, %q) = false, want true", model)
		}
		if caps.ContextWindowTokens != 500000 || caps.MaxOutputTokens != 32768 || caps.DefaultMaxOutputTokens != 32768 {
			t.Fatalf("LookupModelCapabilities(xai, %q) limits = %#v", model, caps)
		}
		if !caps.SupportsImages || !caps.SupportsToolCalls || !caps.SupportsReasoning {
			t.Fatalf("LookupModelCapabilities(xai, %q) capabilities = %#v", model, caps)
		}
		if caps.SupportsJSONOutput || caps.ReasoningMode != ReasoningModeEffort ||
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
		if !sameStrings(caps.ReasoningEfforts, []string{"high", "max"}) {
			t.Fatalf("LookupModelCapabilities(deepseek, %q) efforts = %#v, want high/max", test.model, caps.ReasoningEfforts)
		}
		if levels := ReasoningLevelsForModel("deepseek", test.model); !sameStrings(levels, []string{"none", "high", "max"}) {
			t.Fatalf("ReasoningLevelsForModel(deepseek, %q) = %#v, want none/high/max", test.model, levels)
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
		if !caps.SupportsReasoning || !caps.SupportsToolCalls || !caps.SupportsJSONOutput {
			t.Fatalf("LookupModelCapabilities(xiaomi, %q) caps = %#v, want reasoning/tools/json", tc.model, caps)
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
