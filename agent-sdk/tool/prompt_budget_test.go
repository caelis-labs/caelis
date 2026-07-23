package tool

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEstimateModelPromptTokensMatchesVisibleToolSpecs(t *testing.T) {
	t.Parallel()

	tools := []Tool{NamedTool{Def: Definition{
		Name:        "Probe",
		Description: "Inspect one value.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"value": map[string]any{"type": "string"}},
			"additionalProperties": false,
		},
	}}}
	raw, err := json.Marshal(ModelSpecs(tools))
	if err != nil {
		t.Fatalf("json.Marshal(ModelSpecs()) error = %v", err)
	}
	runes := utf8.RuneCountInString(strings.TrimSpace(string(raw)))
	want := (runes+approxPromptRunesPerToken-1)/approxPromptRunesPerToken + modelPromptToolOverheadTokens
	if got := EstimateModelPromptTokens(tools); got != want {
		t.Fatalf("EstimateModelPromptTokens() = %d, want %d", got, want)
	}
}

func TestEstimateModelPromptTokensOmitsEmptyToolSet(t *testing.T) {
	t.Parallel()

	if got := EstimateModelPromptTokens(nil); got != 0 {
		t.Fatalf("EstimateModelPromptTokens(nil) = %d, want 0", got)
	}
}
