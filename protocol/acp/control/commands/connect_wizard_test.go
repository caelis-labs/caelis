package commands

import "testing"

func TestConnectWizardStateEncodesLegacyCompletionPayload(t *testing.T) {
	state := ConnectWizardState{
		Provider:       "xiaomi",
		BaseURL:        "https://token-plan-cn.xiaomimimo.com/v1",
		TimeoutSeconds: 60,
		TokenRef:       "env:MIMO_TOKEN_PLAN_API_KEY",
		Model:          "mimo-v2.5-pro",
	}

	got := state.EncodeCompletionPayload()
	want := "xiaomi|https%3A%2F%2Ftoken-plan-cn.xiaomimimo.com%2Fv1|60|env%3AMIMO_TOKEN_PLAN_API_KEY|mimo-v2.5-pro"
	if got != want {
		t.Fatalf("EncodeCompletionPayload() = %q, want %q", got, want)
	}
}

func TestParseConnectWizardPayloadDecodesStructuredState(t *testing.T) {
	got := ParseConnectWizardPayload("xiaomi|https%3A%2F%2Ftoken-plan-cn.xiaomimimo.com%2Fv1|60|env%3AMIMO_TOKEN_PLAN_API_KEY|mimo-v2.5-pro")

	if got.Provider != "xiaomi" || got.BaseURL != "https://token-plan-cn.xiaomimimo.com/v1" || got.TimeoutSeconds != 60 || got.TokenRef != "env:MIMO_TOKEN_PLAN_API_KEY" || got.Model != "mimo-v2.5-pro" {
		t.Fatalf("ParseConnectWizardPayload() = %#v, want decoded state", got)
	}
	if got.AuthMode != "env" {
		t.Fatalf("AuthMode = %q, want env", got.AuthMode)
	}
}

func TestConnectWizardStateFromMapParsesOptionalFields(t *testing.T) {
	got := ConnectWizardStateFromMap(map[string]string{
		"provider":              " minimax ",
		"apikey":                "sk-test",
		"context_window_tokens": "2048",
		"max_output_tokens":     "512",
		"reasoning_levels":      "low, high",
	})

	if got.Provider != "minimax" || got.AuthMode != "token" || got.ContextWindowTokens != 2048 || got.MaxOutputTokens != 512 {
		t.Fatalf("ConnectWizardStateFromMap() = %#v, want parsed fields", got)
	}
	if len(got.ReasoningLevels) != 2 || got.ReasoningLevels[0] != "low" || got.ReasoningLevels[1] != "high" {
		t.Fatalf("ReasoningLevels = %#v, want low/high", got.ReasoningLevels)
	}
}
