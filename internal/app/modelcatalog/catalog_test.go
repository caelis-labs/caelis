package modelcatalog

import (
	"slices"
	"testing"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
)

func TestModelsReturnsClone(t *testing.T) {
	models := Models("openai")
	if len(models) == 0 {
		t.Fatal("Models(openai) = empty, want builtin models")
	}
	models[0] = "mutated"
	next := Models("openai")
	if slices.Contains(next, "mutated") {
		t.Fatalf("Models(openai) reused backing slice: %#v", next)
	}
}

func TestLookupCapabilitiesAndReasoningLevels(t *testing.T) {
	caps, ok := LookupCapabilities("deepseek", "deepseek-v4-pro")
	if !ok {
		t.Fatal("LookupCapabilities(deepseek, deepseek-v4-pro) = false, want true")
	}
	if caps.ContextWindowTokens != 1048576 || caps.DefaultReasoningEffort != "high" {
		t.Fatalf("capabilities = %#v, want deepseek defaults", caps)
	}
	levels := ReasoningLevelsFromCapabilities(caps)
	if !slices.Equal(levels, []string{"none", "high", "max"}) {
		t.Fatalf("reasoning levels = %#v, want none/high/max", levels)
	}
}

func TestCapabilitiesFromModelInfoNormalizesReasoning(t *testing.T) {
	caps, ok := CapabilitiesFromModelInfo(coremodel.ModelInfo{
		ID:                     "remote",
		ContextWindowTokens:    200000,
		MaxOutputTokens:        32000,
		SupportsToolCalls:      true,
		ReasoningEfforts:       []string{"High", "none", "high"},
		DefaultReasoningEffort: "HIGH",
	})
	if !ok {
		t.Fatal("CapabilitiesFromModelInfo() = false, want known capabilities")
	}
	if caps.ReasoningMode != ReasoningModeEffort || !slices.Equal(caps.ReasoningEfforts, []string{"high"}) || caps.DefaultReasoningEffort != "high" {
		t.Fatalf("capabilities = %#v, want normalized reasoning", caps)
	}
}
