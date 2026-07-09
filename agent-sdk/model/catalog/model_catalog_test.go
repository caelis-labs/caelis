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

func TestLookupSuggestedModelCapabilitiesSupportsOpenAICompatible(t *testing.T) {
	caps, ok := LookupSuggestedModelCapabilities("openai-compatible", "gpt-4o-mini")
	if !ok {
		t.Fatal("LookupSuggestedModelCapabilities(openai-compatible, gpt-4o-mini) = false, want true")
	}
	if caps.ContextWindowTokens <= 0 {
		t.Fatalf("ContextWindowTokens = %d, want > 0", caps.ContextWindowTokens)
	}
}

func TestLookupSuggestedModelCapabilitiesUsesCodeFreeCatalogForGLM51(t *testing.T) {
	caps, ok := LookupSuggestedModelCapabilities("codefree", "GLM-5.1")
	if !ok {
		t.Fatal("LookupSuggestedModelCapabilities(codefree, GLM-5.1) = false, want true")
	}
	if caps.ContextWindowTokens != 112000 || caps.MaxOutputTokens != 16000 || caps.DefaultMaxOutputTokens != 16000 {
		t.Fatalf("limits = %d/%d default %d, want 112000/16000 default 16000",
			caps.ContextWindowTokens, caps.MaxOutputTokens, caps.DefaultMaxOutputTokens)
	}
	if caps.SupportsReasoning || caps.ReasoningMode != ReasoningModeNone {
		t.Fatalf("reasoning caps = %#v, want no reasoning", caps)
	}
	if levels := ReasoningLevelsForModel("codefree", "GLM-5.1"); len(levels) != 0 {
		t.Fatalf("ReasoningLevelsForModel(codefree, GLM-5.1) = %#v, want empty", levels)
	}
}

func TestListCatalogModelsIncludesBuiltinDefaults(t *testing.T) {
	models := ListCatalogModels("deepseek")
	if len(models) == 0 {
		t.Fatal("ListCatalogModels(deepseek) returned no models")
	}
	foundFlash := false
	foundPro := false
	for _, model := range models {
		switch model {
		case "deepseek-v4-flash":
			foundFlash = true
		case "deepseek-v4-pro":
			foundPro = true
		}
	}
	if !foundFlash || !foundPro {
		t.Fatalf("ListCatalogModels(deepseek) = %#v, want deepseek-v4-flash and deepseek-v4-pro", models)
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

func TestOllamaStaticDefaultsComeFromCatalog(t *testing.T) {
	disableDynamicCatalogForTest(t)

	models := ListCatalogModels("ollama")
	for _, model := range []string{"qwen2.5:7b", "llama3.1:8b", "deepseek-r1:7b", "gemma3:4b"} {
		if !containsString(models, model) {
			t.Fatalf("ListCatalogModels(ollama) = %#v, missing %q", models, model)
		}
		caps, ok := LookupModelCapabilities("ollama", model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(ollama, %q) = false, want true", model)
		}
		if caps.ContextWindowTokens != 128000 || caps.MaxOutputTokens != 32768 {
			t.Fatalf("LookupModelCapabilities(ollama, %q) limits = %d/%d, want 128000/32768",
				model, caps.ContextWindowTokens, caps.MaxOutputTokens)
		}
	}
}

func TestCodeFreeStaticModelsDoNotExposeReasoning(t *testing.T) {
	models := ListCatalogModels("codefree")
	for _, retired := range []string{"DeepSeek-V3.1-Terminus", "GLM-5-ctyun-oc"} {
		if containsString(models, retired) {
			t.Fatalf("ListCatalogModels(codefree) = %#v, did not want retired %q", models, retired)
		}
	}
	tests := []struct {
		model       string
		context     int
		maxOutput   int
		imageInputs bool
	}{
		{model: "DeepSeek-V4-Flash-ctyun-oc", context: 112000, maxOutput: 16000, imageInputs: false},
		{model: "GLM-4.7", context: 80000, maxOutput: 8000, imageInputs: false},
		{model: "GLM-5.1", context: 112000, maxOutput: 16000, imageInputs: false},
		{model: "GLM-5.1-ctyun-oc", context: 112000, maxOutput: 16000, imageInputs: false},
		{model: "Qwen3.5-122B-A10B", context: 112000, maxOutput: 16000, imageInputs: true},
	}
	for _, tt := range tests {
		if !containsString(models, tt.model) {
			t.Fatalf("ListCatalogModels(codefree) = %#v, missing %q", models, tt.model)
		}
		caps, ok := LookupModelCapabilities("codefree", tt.model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(codefree, %q) = false, want true", tt.model)
		}
		if caps.SupportsReasoning || caps.ReasoningMode != ReasoningModeNone {
			t.Fatalf("LookupModelCapabilities(codefree, %q) = %#v, want no reasoning", tt.model, caps)
		}
		if caps.ContextWindowTokens != tt.context || caps.MaxOutputTokens != tt.maxOutput || caps.DefaultMaxOutputTokens != tt.maxOutput {
			t.Fatalf("LookupModelCapabilities(codefree, %q) limits = %d/%d default %d, want %d/%d default %d",
				tt.model, caps.ContextWindowTokens, caps.MaxOutputTokens, caps.DefaultMaxOutputTokens, tt.context, tt.maxOutput, tt.maxOutput)
		}
		if caps.SupportsImages != tt.imageInputs {
			t.Fatalf("LookupModelCapabilities(codefree, %q).SupportsImages = %v, want %v", tt.model, caps.SupportsImages, tt.imageInputs)
		}
		if len(caps.ReasoningEfforts) != 0 || caps.DefaultReasoningEffort != "" {
			t.Fatalf("LookupModelCapabilities(codefree, %q) efforts = %#v/%q, want none", tt.model, caps.ReasoningEfforts, caps.DefaultReasoningEffort)
		}
		if levels := ReasoningLevelsForModel("codefree", tt.model); len(levels) != 0 {
			t.Fatalf("ReasoningLevelsForModel(codefree, %q) = %#v, want empty", tt.model, levels)
		}
	}
}

func TestDeepSeekStaticModelsExposeThinkingEfforts(t *testing.T) {
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		caps, ok := LookupModelCapabilities("deepseek", model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(deepseek, %q) = false, want true", model)
		}
		if !caps.SupportsReasoning || caps.ReasoningMode != ReasoningModeToggle {
			t.Fatalf("LookupModelCapabilities(deepseek, %q) = %#v, want toggle reasoning", model, caps)
		}
		if caps.ContextWindowTokens != 1048576 || caps.MaxOutputTokens != 393216 || caps.DefaultMaxOutputTokens != 32768 {
			t.Fatalf("LookupModelCapabilities(deepseek, %q) limits = %d/%d default %d, want 1048576/393216 default 32768",
				model, caps.ContextWindowTokens, caps.MaxOutputTokens, caps.DefaultMaxOutputTokens)
		}
		if !sameStrings(caps.ReasoningEfforts, []string{"high", "max"}) {
			t.Fatalf("LookupModelCapabilities(deepseek, %q) efforts = %#v, want high/max", model, caps.ReasoningEfforts)
		}
		if levels := ReasoningLevelsForModel("deepseek", model); !sameStrings(levels, []string{"none", "high", "max"}) {
			t.Fatalf("ReasoningLevelsForModel(deepseek, %q) = %#v, want none/high/max", model, levels)
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
		"codefree:remote-codefree-model": {
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

	for _, provider := range []string{"openai", "codefree", "minimax"} {
		models := ListCatalogModels(provider)
		for _, model := range models {
			switch model {
			case "gpt-from-remote", "gpt-from-embedded", "remote-codefree-model", "remote-minimax-model":
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

	openAICompatModels := ListModelDirectoryModels("openai-compatible")
	for _, want := range []string{"gpt-from-local", "gpt-from-remote", "gpt-from-embedded"} {
		if !containsString(openAICompatModels, want) {
			t.Fatalf("ListModelDirectoryModels(openai-compatible) = %#v, missing %q", openAICompatModels, want)
		}
	}
	if containsString(openAICompatModels, "accidental-substring-match") {
		t.Fatalf("ListModelDirectoryModels(openai-compatible) = %#v, included substring provider match", openAICompatModels)
	}
	for _, stale := range []string{"gpt-from-remote", "gpt-from-embedded"} {
		if containsString(ListCatalogModels("openai-compatible"), stale) {
			t.Fatalf("ListCatalogModels(openai-compatible) included dynamic model %q", stale)
		}
	}

	anthropicCompatModels := ListModelDirectoryModels("anthropic-compatible")
	if !containsString(anthropicCompatModels, "claude-from-remote") {
		t.Fatalf("ListModelDirectoryModels(anthropic-compatible) = %#v, missing claude-from-remote", anthropicCompatModels)
	}
	openRouterModels := ListModelDirectoryModels("openrouter")
	if !containsString(openRouterModels, "openai/gpt-from-openrouter") {
		t.Fatalf("ListModelDirectoryModels(openrouter) = %#v, missing openai/gpt-from-openrouter", openRouterModels)
	}
	geminiModels := ListModelDirectoryModels("gemini")
	if !containsString(geminiModels, "gemini-from-google") {
		t.Fatalf("ListModelDirectoryModels(gemini) = %#v, missing aliased google model", geminiModels)
	}
	if ProviderUsesModelDirectory("gemini") {
		t.Fatal("ProviderUsesModelDirectory(gemini) = true, want false for explicit provider catalog recommendations")
	}
	for _, provider := range []string{"openai-compatible", "anthropic-compatible", "openrouter"} {
		if !ProviderUsesModelDirectory(provider) {
			t.Fatalf("ProviderUsesModelDirectory(%q) = false, want true", provider)
		}
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
