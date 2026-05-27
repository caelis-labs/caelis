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

func TestLookupSuggestedModelCapabilitiesUsesCodeFreeOverlayForGLM51(t *testing.T) {
	caps, ok := LookupSuggestedModelCapabilities("codefree", "GLM-5.1")
	if !ok {
		t.Fatal("LookupSuggestedModelCapabilities(codefree, GLM-5.1) = false, want true")
	}
	if caps.ContextWindowTokens != 128000 {
		t.Fatalf("ContextWindowTokens = %d, want 128000", caps.ContextWindowTokens)
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

func TestCodeFreeStaticModelsDoNotExposeReasoning(t *testing.T) {
	models := ListCatalogModels("codefree")
	want := []string{"GLM-4.7", "DeepSeek-V3.1-Terminus", "Qwen3.5-122B-A10B", "GLM-5.1"}
	for _, model := range want {
		if !containsString(models, model) {
			t.Fatalf("ListCatalogModels(codefree) = %#v, missing %q", models, model)
		}
		caps, ok := LookupModelCapabilities("codefree", model)
		if !ok {
			t.Fatalf("LookupModelCapabilities(codefree, %q) = false, want true", model)
		}
		if caps.SupportsReasoning || caps.ReasoningMode != ReasoningModeNone {
			t.Fatalf("LookupModelCapabilities(codefree, %q) = %#v, want no reasoning", model, caps)
		}
		if caps.ContextWindowTokens != 128000 {
			t.Fatalf("LookupModelCapabilities(codefree, %q).ContextWindowTokens = %d, want 128000", model, caps.ContextWindowTokens)
		}
		if len(caps.ReasoningEfforts) != 0 || caps.DefaultReasoningEffort != "" {
			t.Fatalf("LookupModelCapabilities(codefree, %q) efforts = %#v/%q, want none", model, caps.ReasoningEfforts, caps.DefaultReasoningEffort)
		}
		if levels := ReasoningLevelsForModel("codefree", model); len(levels) != 0 {
			t.Fatalf("ReasoningLevelsForModel(codefree, %q) = %#v, want empty", model, levels)
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
	wantModels := []string{"mimo-v2.5-pro", "mimo-v2-pro", "mimo-v2.5", "mimo-v2-omni", "mimo-v2-flash"}
	for _, model := range wantModels {
		if !containsString(models, model) {
			t.Fatalf("ListCatalogModels(xiaomi) = %#v, missing %q", models, model)
		}
	}
	for _, model := range []string{"mimo-v2-reasoner", "MiMo-VL-7B-RL"} {
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
		{model: "mimo-v2-pro", context: 1048576, maxOutput: 131072},
		{model: "mimo-v2.5", context: 1048576, maxOutput: 131072, imageInputs: true},
		{model: "mimo-v2-omni", context: 262144, maxOutput: 131072, imageInputs: true},
		{model: "mimo-v2-flash", context: 262144, maxOutput: 65536},
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
