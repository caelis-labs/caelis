package services

import "testing"

func TestConnectWizardStateEncodesCompletionPayload(t *testing.T) {
	state := ConnectWizardState{
		Provider:       "xiaomi",
		BaseURL:        ConnectXiaomiTokenPlanCNBaseURL,
		TimeoutSeconds: 60,
		TokenRef:       "env:MIMO_TOKEN_PLAN_API_KEY",
		Model:          "mimo-v2.5-pro",
	}
	want := "xiaomi|https%3A%2F%2Ftoken-plan-cn.xiaomimimo.com%2Fv1|60|env%3AMIMO_TOKEN_PLAN_API_KEY|mimo-v2.5-pro"
	if got := state.EncodeCompletionPayload(); got != want {
		t.Fatalf("EncodeCompletionPayload() = %q, want %q", got, want)
	}
}

func TestParseConnectWizardPayloadDecodesStructuredState(t *testing.T) {
	got := ParseConnectWizardPayload("xiaomi|https%3A%2F%2Ftoken-plan-cn.xiaomimimo.com%2Fv1|60|env%3AMIMO_TOKEN_PLAN_API_KEY|mimo-v2.5-pro")
	if got.Provider != "xiaomi" || got.BaseURL != ConnectXiaomiTokenPlanCNBaseURL || got.TimeoutSeconds != 60 || got.AuthMode != "env" || got.TokenRef != "env:MIMO_TOKEN_PLAN_API_KEY" || got.Model != "mimo-v2.5-pro" {
		t.Fatalf("ParseConnectWizardPayload() = %#v, want decoded state", got)
	}
}

func TestConnectWizardStateFromMapParsesOptionalFields(t *testing.T) {
	got := ConnectWizardStateFromMap(map[string]string{
		"provider":              "minimax",
		"timeout":               "120",
		"apikey":                "sk-test",
		"model":                 "MiniMax-M2.7-highspeed",
		"context_window_tokens": "204800",
		"max_output_tokens":     "8192",
		"reasoning_levels":      "low,medium",
	})
	if got.Provider != "minimax" || got.TimeoutSeconds != 120 || got.AuthMode != "token" || got.TokenRef != "sk-test" || got.ContextWindowTokens != 204800 || got.MaxOutputTokens != 8192 || len(got.ReasoningLevels) != 2 {
		t.Fatalf("ConnectWizardStateFromMap() = %#v, want parsed fields", got)
	}
}

func TestConnectWizardFlowHelpersUseSharedProviderCatalog(t *testing.T) {
	if !ConnectWizardProviderHasEndpointStep("xiaomi") || !ConnectWizardProviderHasEndpointStep("volcengine") {
		t.Fatal("endpoint step helpers should follow catalog-backed endpoint providers")
	}
	if ConnectWizardProviderHasEndpointStep("minimax") {
		t.Fatal("minimax should not expose endpoint step")
	}
	if !ConnectWizardProviderHasBaseURLStep("openai-compatible") || !ConnectWizardProviderHasBaseURLStep("anthropic-compatible") {
		t.Fatal("compatible providers should expose base URL step")
	}
	if got := ConnectWizardTokenEnvHint(map[string]string{
		"provider": "xiaomi",
		"baseurl":  "https://token-plan-cn.xiaomimimo.com/custom/path",
	}); got != "MIMO_TOKEN_PLAN_API_KEY" {
		t.Fatalf("ConnectWizardTokenEnvHint() = %q, want token-plan env", got)
	}
}

func TestBuildConnectWizardExecLineUsesSharedCommandShape(t *testing.T) {
	got := BuildConnectWizardExecLine(map[string]string{
		"provider":              "xiaomi",
		"model":                 "mimo-v2.5-pro",
		"baseurl":               ConnectXiaomiTokenPlanCNBaseURL,
		"apikey":                "env:MIMO_TOKEN_PLAN_API_KEY",
		"context_window_tokens": "",
		"max_output_tokens":     "",
		"reasoning_levels":      "",
	})
	want := "/connect xiaomi mimo-v2.5-pro " + ConnectXiaomiTokenPlanCNBaseURL + " 60 env:MIMO_TOKEN_PLAN_API_KEY - - -"
	if got != want {
		t.Fatalf("BuildConnectWizardExecLine() = %q, want %q", got, want)
	}
}
