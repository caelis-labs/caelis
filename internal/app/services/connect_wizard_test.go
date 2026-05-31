package services

import (
	"context"
	"reflect"
	"testing"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

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

func TestDefaultConnectWizardFlowDefinesSharedShellShape(t *testing.T) {
	flow := DefaultConnectWizardFlow()
	if flow.Command != "connect" || flow.DisplayLine != "/connect" {
		t.Fatalf("flow = %#v, want connect display flow", flow)
	}
	keys := make([]string, 0, len(flow.Steps))
	validators := map[string]string{}
	for _, step := range flow.Steps {
		keys = append(keys, step.Key)
		validators[step.Key] = step.Validator
	}
	want := []string{"provider", "endpoint", "baseurl", "apikey", "model", "context_window_tokens", "max_output_tokens", "reasoning_levels"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("connect wizard keys = %#v, want %#v", keys, want)
	}
	if validators["context_window_tokens"] != appviewmodel.WizardValidatorInt || validators["max_output_tokens"] != appviewmodel.WizardValidatorInt {
		t.Fatalf("validators = %#v, want int validation for token fields", validators)
	}
	if !flow.Steps[0].RequireCandidate || !flow.Steps[3].HideInput || !flow.Steps[3].DynamicFreeformHint {
		t.Fatalf("flow steps = %#v, want provider picker and hidden dynamic API key step", flow.Steps)
	}
}

func TestModelServiceConnectWizardExposesSharedFlow(t *testing.T) {
	svc, err := New(Config{Engine: &recordingEngine{}})
	if err != nil {
		t.Fatal(err)
	}
	flow, err := svc.Models().ConnectWizard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(flow, DefaultConnectWizardFlow()) {
		t.Fatalf("ConnectWizard() = %#v, want default shared flow", flow)
	}
}

func TestConnectWizardSharedStepBehavior(t *testing.T) {
	state := map[string]string{"provider": "xiaomi"}
	if !ConnectWizardShouldSkip("baseurl", state) || ConnectWizardShouldSkip("endpoint", state) {
		t.Fatalf("xiaomi skip behavior wrong for state %#v", state)
	}
	ConfirmConnectWizardStep("endpoint", ConnectXiaomiTokenPlanCNBaseURL, &ConnectWizardConfirmCandidate{NoAuth: true}, state)
	if state["baseurl"] != ConnectXiaomiTokenPlanCNBaseURL || state["_reuseauth"] != "true" {
		t.Fatalf("endpoint confirm state = %#v, want baseurl plus reuse auth", state)
	}
	if !ConnectWizardShouldSkip("apikey", state) {
		t.Fatalf("apikey should be skipped after reusable endpoint auth: %#v", state)
	}
	state["model"] = "mimo-v2.5-pro"
	got := ConnectWizardCompletionCommand("model", state)
	want := "connect-model:xiaomi|https%3A%2F%2Ftoken-plan-cn.xiaomimimo.com%2Fv1|60||mimo-v2.5-pro"
	if got != want {
		t.Fatalf("model completion command = %q, want %q", got, want)
	}
	ConfirmConnectWizardStep("model", "mimo-v2.5-pro", &ConnectWizardConfirmCandidate{Value: "mimo-v2.5-pro"}, state)
	if state["_known_model"] != "true" || !ConnectWizardShouldSkip("context_window_tokens", state) {
		t.Fatalf("model confirm state = %#v, want known model and token fields skipped", state)
	}
	ConfirmConnectWizardStep("provider", " OLLAMA ", &ConnectWizardConfirmCandidate{NoAuth: true}, state)
	if state["provider"] != "ollama" || state["_noauth"] != "true" || state["_reuseauth"] != "" {
		t.Fatalf("provider confirm state = %#v, want normalized no-auth provider", state)
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
