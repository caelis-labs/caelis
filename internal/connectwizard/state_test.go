package connectwizard

import (
	"strings"
	"testing"
)

func TestConnectWizardStateEncodesStructuredCompletionState(t *testing.T) {
	state := ConnectWizardState{
		Provider:       "xiaomi",
		BaseURL:        "https://token-plan-cn.xiaomimimo.com/v1",
		TimeoutSeconds: 60,
		TokenRef:       "env:MIMO_TOKEN_PLAN_API_KEY",
		Model:          "mimo-v2.5-pro",
	}

	got := state.EncodeCompletionState()
	if got == "" || strings.Contains(got, "|") {
		t.Fatalf("EncodeCompletionState() = %q, want escaped JSON payload", got)
	}
	decoded := ParseConnectWizardStatePayload(got)
	if decoded.Provider != state.Provider || decoded.BaseURL != state.BaseURL || decoded.TimeoutSeconds != state.TimeoutSeconds || decoded.TokenRef != state.TokenRef || decoded.Model != state.Model {
		t.Fatalf("round-tripped state = %#v, want %#v", decoded, state)
	}
	if decoded.AuthMode != "env" {
		t.Fatalf("AuthMode = %q, want env", decoded.AuthMode)
	}
}

func TestParseConnectWizardStatePayloadDecodesStructuredState(t *testing.T) {
	got := ParseConnectWizardStatePayload("%7B%22provider%22%3A%22xiaomi%22%2C%22base_url%22%3A%22https%3A%2F%2Ftoken-plan-cn.xiaomimimo.com%2Fv1%22%2C%22timeout_seconds%22%3A60%2C%22token_ref%22%3A%22env%3AMIMO_TOKEN_PLAN_API_KEY%22%2C%22model%22%3A%22mimo-v2.5-pro%22%7D")

	if got.Provider != "xiaomi" || got.BaseURL != "https://token-plan-cn.xiaomimimo.com/v1" || got.TimeoutSeconds != 60 || got.TokenRef != "env:MIMO_TOKEN_PLAN_API_KEY" || got.Model != "mimo-v2.5-pro" {
		t.Fatalf("ParseConnectWizardStatePayload() = %#v, want decoded state", got)
	}
	if got.AuthMode != "env" {
		t.Fatalf("AuthMode = %q, want env", got.AuthMode)
	}
}

func TestParseConnectWizardStatePayloadRejectsLegacyPipePayload(t *testing.T) {
	got := ParseConnectWizardStatePayload("xiaomi|https%3A%2F%2Ftoken-plan-cn.xiaomimimo.com%2Fv1|60|env%3AMIMO_TOKEN_PLAN_API_KEY|mimo-v2.5-pro")
	if got.Provider != "" || got.BaseURL != "" || got.TokenRef != "" || got.Model != "" {
		t.Fatalf("ParseConnectWizardStatePayload() = %#v, want empty state for legacy pipe payload", got)
	}
	if got.TimeoutSeconds != DefaultConnectTimeoutSeconds {
		t.Fatalf("TimeoutSeconds = %d, want default", got.TimeoutSeconds)
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
