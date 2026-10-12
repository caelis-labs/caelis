package modelcatalog

import (
	"encoding/json"
	"slices"
	"testing"
)

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

func TestCoreCatalogIsOneValidatedSource(t *testing.T) {
	var entries []catalogEntry
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		key := e.Provider + ":" + e.Model
		if seen[key] || e.Model == "" || e.ContextWindowTokens <= 0 || e.DefaultMaxOutputTokens <= 0 || !e.SupportsToolCalls {
			t.Fatalf("invalid catalog entry: %s", key)
		}
		seen[key] = true
		switch e.Provider {
		case "openai-codex", "xai", "deepseek", "xiaomi":
		default:
			t.Fatalf("non-core provider %s", e.Provider)
		}
		caps, ok := LookupModelCapabilities(e.Provider, e.Model)
		if !ok || caps.ContextWindowTokens != e.ContextWindowTokens {
			t.Fatalf("lookup differs from directory: %s", key)
		}
		if e.DefaultReasoningEffort != "" && !slices.Contains(e.ReasoningEfforts, e.DefaultReasoningEffort) {
			t.Fatalf("unsupported default effort: %s", key)
		}
	}
	if len(ListRecommendedModels("openai-codex")) != 4 {
		t.Fatal("subscription recommendations missing")
	}
	for _, provider := range []string{"openai", "openrouter", "anthropic", "magpie", "custom"} {
		if len(ListRecommendedModels(provider)) != 0 {
			t.Fatalf("unexpected maintained directory for %s", provider)
		}
		if _, ok := LookupModelCapabilities(provider, "gpt-6.1-sol"); ok {
			t.Fatalf("inherited native metadata: %s", provider)
		}
	}

}
func TestCatalogLookupReturnsDetachedCapabilities(t *testing.T) {
	a, _ := LookupModelCapabilities("openai-codex", "gpt-6.1-sol")
	a.ReasoningEfforts[0] = "invalid"
	b, _ := LookupModelCapabilities("openai-codex", "gpt-6.1-sol")
	if b.ReasoningEfforts[0] == "invalid" {
		t.Fatal("caller changed catalog")
	}
}
func sameStrings(a, b []string) bool { return slices.Equal(a, b) }
