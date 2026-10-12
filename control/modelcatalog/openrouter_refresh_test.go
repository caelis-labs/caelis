package modelcatalog

import (
	"reflect"
	"testing"
)

func TestOpenRouterDirectoryExclusionsPreserveLookupAndLocalOverrides(t *testing.T) {
	disableDynamicCatalogForTest(t)
	dynamicMu.Lock()
	embeddedCatalog = capSnapshot{
		"openrouter:vendor/old":     {ContextWindow: 64000, Images: true, HiddenFromDirectory: true},
		"openrouter:vendor/current": {ContextWindow: 128000, ToolCalls: true},
	}
	remoteCatalog = capSnapshot{
		"openrouter:vendor/old":    {ContextWindow: 64000, Images: true},
		"openrouter:vendor/remote": {ContextWindow: 64000},
	}
	dynamicMu.Unlock()
	if got := ListModelDirectoryModels("openrouter"); !reflect.DeepEqual(got, []string{"vendor/current", "vendor/remote"}) {
		t.Fatalf("remote reintroduced excluded route: %v", got)
	}
	if caps, ok := LookupModelCapabilities("openrouter", "vendor/old"); !ok || caps.ContextWindowTokens != 64000 || !caps.SupportsImages {
		t.Fatalf("historical lookup = %#v, %t", caps, ok)
	}
	dynamicMu.Lock()
	localOverrides = capSnapshot{"openrouter:vendor/old": {ContextWindow: 500000}}
	dynamicMu.Unlock()
	if !containsString(ListModelDirectoryModels("openrouter"), "vendor/old") {
		t.Fatal("explicit local override did not restore directory choice")
	}
	if caps, _ := LookupModelCapabilities("openrouter", "vendor/old"); caps.ContextWindowTokens != 500000 {
		t.Fatalf("local capability priority lost: %#v", caps)
	}
}

func TestOpenRouterRefreshedMetadataRemainsRouteScoped(t *testing.T) {
	disableDynamicCatalogForTest(t)
	dynamicMu.Lock()
	embeddedCatalog = parseEmbeddedSnapshotBytes(embeddedCatalogSnapshot)
	dynamicMu.Unlock()
	for _, tc := range []struct {
		name           string
		window, output int
		efforts        []string
		defaultEffort  string
	}{
		{name: "openai/gpt-6.1-sol", window: 1050000, output: 128000, efforts: []string{"max", "xhigh", "high", "medium", "low"}, defaultEffort: "medium"},
		{name: "anthropic/claude-haiku-5.5", window: 1000000, output: 128000, efforts: []string{"max", "xhigh", "high", "medium", "low"}, defaultEffort: "medium"},
		{name: "mistralai/mistral-large-4-0", window: 1048576, output: 262144, efforts: []string{"high", "none"}, defaultEffort: "high"},
		{name: "xiaomi/mimo-v2.6-pro", window: 1048576, output: 131072},
	} {
		caps, ok := LookupModelCapabilities("openrouter", tc.name)
		if !ok || caps.ContextWindowTokens != tc.window || caps.MaxOutputTokens != tc.output || !caps.SupportsImages || !caps.SupportsToolCalls ||
			!reflect.DeepEqual(ReasoningLevelsForModel("openrouter", tc.name), tc.efforts) || DefaultReasoningEffortForModel("openrouter", tc.name) != tc.defaultEffort {
			t.Fatalf("%s: %#v, %t", tc.name, caps, ok)
		}
		if !containsString(ListModelDirectoryModels("openrouter"), tc.name) {
			t.Fatalf("current route %q missing from directory", tc.name)
		}
	}
	if caps, ok := LookupModelCapabilities("openai-compatible", "openai/gpt-6.1-sol"); ok {
		t.Fatalf("route metadata leaked into custom proxy: %#v", caps)
	}
}
