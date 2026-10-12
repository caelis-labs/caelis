package modelcatalog

import "testing"

func TestSupersededRecommendationsRetainExplicitCapabilities(t *testing.T) {
	disableDynamicCatalogForTest(t)
	for _, tc := range []struct {
		provider string
		name     string
		window   int
	}{
		{provider: "anthropic", name: "claude-sonnet-5", window: 1000000},
		{provider: "anthropic", name: "claude-haiku-4-5", window: 200000},
		{provider: "gemini", name: "gemini-3.7-flash", window: 1048576},
		{provider: "gemini", name: "gemini-2.0-flash", window: 1048576},
		{provider: "anthropic", name: "claude-3-5-haiku", window: 200000},
	} {
		t.Run(tc.provider+"/"+tc.name, func(t *testing.T) {
			if containsString(ListRecommendedModels(tc.provider), tc.name) {
				t.Fatal("superseded model is still recommended")
			}
			caps, ok := LookupModelCapabilities(tc.provider, tc.name)
			if !ok || caps.ContextWindowTokens != tc.window || !caps.SupportsToolCalls || !caps.SupportsImages {
				t.Fatalf("explicit capabilities = %#v, %t", caps, ok)
			}
		})
	}
	for _, name := range []string{"gemini-3.8-flash", "gemini-3.6-flash", "gemini-3.5-flash-lite", "gemini-3.1-pro-preview", "gemini-3.1-pro-preview-customtools"} {
		if !containsString(ListRecommendedModels("gemini"), name) {
			t.Fatalf("available model %q lost its recommendation", name)
		}
	}
}

func TestMistralLarge4CloudCapabilitiesStayEndpointScoped(t *testing.T) {
	disableDynamicCatalogForTest(t)
	if !containsString(ListOllamaCloudModels(), "mistral-large-4") {
		t.Fatal("Mistral Large 4 missing from direct Cloud choices")
	}
	caps, ok := LookupModelCapabilities(OllamaCloudProvider, "mistral-large-4")
	if !ok || caps.ContextWindowTokens != 1000000 || caps.MaxOutputTokens != 0 || caps.DefaultMaxOutputTokens != 32768 ||
		!caps.SupportsImages || !caps.SupportsToolCalls || caps.ReasoningMode != ReasoningModeToggle {
		t.Fatalf("Cloud capabilities = %#v, %t", caps, ok)
	}
	for _, provider := range []string{"ollama", "openai-compatible", "openai-responses-compatible", "openrouter"} {
		if caps, ok := LookupModelCapabilities(provider, "mistral-large-4"); ok {
			t.Fatalf("%s inherited direct Cloud metadata: %#v", provider, caps)
		}
	}
}
