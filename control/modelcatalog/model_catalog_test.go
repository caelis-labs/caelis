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

	caps, ok := LookupModelCapabilities("openai", "gpt-6-sol")
	if !ok {
		t.Fatal("LookupModelCapabilities(openai, gpt-6-sol) = false, want builtin fallback")
	}
	if caps.ContextWindowTokens <= 0 || caps.DefaultMaxOutputTokens <= 0 {
		t.Fatalf("caps = %#v, want populated builtin fallback", caps)
	}
}

func TestOpenAICatalogMaintainsOnlyCurrentModels(t *testing.T) {
	disableDynamicCatalogForTest(t)
	want := []string{"gpt-6-astra", "gpt-6-luna", "gpt-6-sol"}
	if got := ListCatalogModels("openai"); !sameStrings(got, want) {
		t.Fatalf("OpenAI catalog = %v, want %v", got, want)
	}
	if got := ListRecommendedModels("openai"); !sameStrings(got, want) {
		t.Fatalf("OpenAI recommendations = %v, want %v", got, want)
	}
	for _, name := range []string{"gpt-6-sol", "gpt-6-luna"} {
		t.Run(name, func(t *testing.T) {
			caps, ok := LookupModelCapabilities("openai", name)
			if !ok || caps.ContextWindowTokens != 1050000 || caps.MaxOutputTokens != 128000 || caps.DefaultMaxOutputTokens != 32768 {
				t.Fatalf("limits = %+v, found=%v", caps, ok)
			}
			if !caps.SupportsImages || !caps.SupportsToolCalls || !caps.SupportsJSONOutput || !caps.SupportsReasoning {
				t.Fatalf("capabilities = %+v, want vision, tools, JSON, and reasoning", caps)
			}
			if caps.ReasoningMode != ReasoningModeEffort || caps.DefaultReasoningEffort != "medium" ||
				!sameStrings(caps.ReasoningEfforts, []string{"none", "low", "medium", "high", "xhigh", "max"}) {
				t.Fatalf("API reasoning = %+v", caps)
			}
		})
	}
}

func TestLookupSuggestedModelCapabilitiesDoesNotInheritVendorForCompatibleEndpoints(t *testing.T) {
	for _, test := range []struct {
		provider string
		model    string
	}{
		{provider: "openai-compatible", model: "gpt-4o-mini"},
		{provider: "openai-responses-compatible", model: "gpt-4o-mini"},
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

func TestBuiltinLookupUsesLongestPrefixWithinProvider(t *testing.T) {
	saved := builtinCatalog
	builtinCatalog = []catalogEntry{
		{provider: "test", pattern: "model", caps: ModelCapabilities{ContextWindowTokens: 1000}},
		{provider: "test", pattern: "model-pro", caps: ModelCapabilities{ContextWindowTokens: 2000}},
		{provider: "other", pattern: "model-pro-dated", caps: ModelCapabilities{ContextWindowTokens: 3000}},
	}
	t.Cleanup(func() { builtinCatalog = saved })

	caps, ok := lookupBuiltin("test", "model-pro-dated")
	if !ok || caps.ContextWindowTokens != 2000 {
		t.Fatalf("lookup = %#v, %v; want longest prefix belonging to the requested provider", caps, ok)
	}
}

func TestReasoningLevelsFromCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps ModelCapabilities
		want []string
	}{
		{name: "none", caps: ModelCapabilities{ReasoningMode: ReasoningModeNone}},
		{name: "toggle", caps: ModelCapabilities{SupportsReasoning: true, ReasoningMode: ReasoningModeToggle}, want: []string{"none", "high"}},
		{name: "toggle with efforts", caps: ModelCapabilities{SupportsReasoning: true, ReasoningMode: ReasoningModeToggle, ReasoningEfforts: []string{"low", "max"}}, want: []string{"none", "low", "max"}},
		{name: "effort", caps: ModelCapabilities{SupportsReasoning: true, ReasoningMode: ReasoningModeEffort, ReasoningEfforts: []string{"low", "xhigh"}}, want: []string{"low", "xhigh"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reasoningLevelsFromCapabilities(tc.caps); !sameStrings(got, tc.want) {
				t.Fatalf("reasoning levels = %v, want %v", got, tc.want)
			}
		})
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
	disableDynamicCatalogForTest(t)
	savedBuiltin, savedOverlay := builtinCatalog, providerOverlayCatalog
	builtinCatalog = []catalogEntry{
		{provider: "test", pattern: "current", caps: ModelCapabilities{ContextWindowTokens: 1000}},
		{provider: "test", pattern: "legacy", hiddenFromRecommendations: true, caps: ModelCapabilities{ContextWindowTokens: 500}},
	}
	providerOverlayCatalog = overlaySnapshot{"test:legacy": {ReasoningMode: ReasoningModeToggle}}
	t.Cleanup(func() { builtinCatalog, providerOverlayCatalog = savedBuiltin, savedOverlay })

	if models := ListRecommendedModels("test"); !sameStrings(models, []string{"current"}) {
		t.Fatalf("recommendations = %v, want hidden entry excluded even with an overlay", models)
	}
	if _, ok := LookupModelCapabilities("test", "legacy"); !ok {
		t.Fatal("hidden entry must remain available for capability lookup")
	}
	dynamicMu.Lock()
	localOverrides = capSnapshot{"test:legacy": {ContextWindow: 500}}
	dynamicMu.Unlock()
	if models := ListRecommendedModels("test"); !sameStrings(models, []string{"current", "legacy"}) {
		t.Fatalf("recommendations = %v, want local override to re-add hidden entry", models)
	}
}

func TestSpeedModesForModelUsesLocalOverrideMetadata(t *testing.T) {
	dynamicMu.Lock()
	savedLocal := localOverrides
	localOverrides = parseSnapshotBytes([]byte(`{
		"openai:gpt-6-sol": {
			"speed_modes": [
				{"level": "FAST", "description": "local priority hint"}
			]
		},
		"openai:gpt-6-astra": {
			"speed_modes": [
				{"level": "fast"}
			]
		}
	}`))
	dynamicMu.Unlock()
	t.Cleanup(func() {
		dynamicMu.Lock()
		localOverrides = savedLocal
		dynamicMu.Unlock()
	})

	modes := SpeedModesForModel("openai", "gpt-6-sol")
	if len(modes) != 1 || modes[0].Level != "fast" || modes[0].Description != "local priority hint" {
		t.Fatalf("SpeedModesForModel(openai, gpt-6-sol) = %#v; want normalized local override", modes)
	}
	if modes := SpeedModesForModel("openai", "gpt-6-astra"); len(modes) != 0 {
		t.Fatalf("SpeedModesForModel(openai, gpt-6-astra) = %#v; want incomplete local override rejected", modes)
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
		"openai:gpt-6-sol": {
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

	caps, ok := LookupModelCapabilities("openai", "gpt-6-sol")
	if !ok {
		t.Fatal("LookupModelCapabilities(openai, gpt-6-sol) = false, want builtin")
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
