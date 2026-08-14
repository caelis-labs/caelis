package modelconfig

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestGrokOAuthProviderTemplate(t *testing.T) {
	template, ok := LookupProvider("grok")
	if !ok {
		t.Fatal("LookupProvider(grok) = not found")
	}
	if template.Provider != "xai" || template.API != model.APIXAIResponses || template.AuthType != model.AuthOAuthToken {
		t.Fatalf("template = %#v", template)
	}
	if template.AuthFlow != AuthFlowGrokOAuth || template.DefaultBaseURL != GrokOAuthBaseURL || !template.PreserveModelOrder {
		t.Fatalf("oauth template = %#v", template)
	}
	alias, ok := LookupProvider("xai")
	if !ok || alias.Label != "grok" {
		t.Fatalf("LookupProvider(xai) = %#v, %v", alias, ok)
	}
}

func TestAssembleConnectBuildsManagedGrokOAuthProfiles(t *testing.T) {
	var authCalls int
	imageInput := true
	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider:   "grok",
		HTTPClient: &http.Client{},
		Models: []ModelSelection{
			{Name: "grok-4.5"},
			{Name: "grok-account-preview", ReasoningEffort: "medium", ImageInput: &imageInput},
		},
	}, ConnectOptions{
		Authenticate: func(_ context.Context, req AuthenticateRequest) error {
			authCalls++
			if req.Provider != "xai" || req.BaseURL != GrokOAuthBaseURL {
				t.Fatalf("authenticate request = %#v", req)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("AssembleConnect() error = %v", err)
	}
	if authCalls != 1 || len(configs) != 2 {
		t.Fatalf("auth calls = %d configs = %d", authCalls, len(configs))
	}
	for _, cfg := range configs {
		if cfg.Provider != "xai" || cfg.API != model.APIXAIResponses || cfg.AuthType != model.AuthOAuthToken {
			t.Fatalf("config = %#v", cfg)
		}
		if cfg.BaseURL != GrokOAuthBaseURL || cfg.CredentialRef != GrokOAuthCredentialRef {
			t.Fatalf("managed endpoint = %#v", cfg)
		}
		if cfg.Token != "" || cfg.PersistToken {
			t.Fatalf("credential material leaked into config = %#v", cfg)
		}
	}
	if got := configs[0]; got.ContextWindowTokens != 500000 || got.MaxOutputTok != 32768 ||
		got.ReasoningEffort != "high" || strings.Join(got.ReasoningLevels, ",") != "low,medium,high" {
		t.Fatalf("grok-4.5 defaults = %#v", got)
	}
	if configs[0].ImageInput != nil {
		t.Fatalf("grok-4.5 image override = %v, want maintained catalog capability", configs[0].ImageInput)
	}
	if configs[1].ImageInput == nil || !*configs[1].ImageInput {
		t.Fatalf("unknown account model image override = %v, want explicit capability", configs[1].ImageInput)
	}
	resolution, err := BuildModel(configs[1], 0, 0)
	if err != nil {
		t.Fatalf("BuildModel(grok account model) error = %v", err)
	}
	capabilities, declared := model.CapabilitiesOf(resolution.Model)
	if !declared || !capabilities.ImageInput {
		t.Fatalf("grok account model capabilities = %#v, declared = %v, want image input", capabilities, declared)
	}
}

func TestAssembleConnectRejectsCustomGrokOAuthEndpoint(t *testing.T) {
	_, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "grok",
		BaseURL:  "https://proxy.example/v1",
		Models:   []ModelSelection{{Name: "grok-4.5"}},
	}, ConnectOptions{Authenticate: func(context.Context, AuthenticateRequest) error {
		t.Fatal("authentication ran before endpoint validation")
		return nil
	}})
	if err == nil || !strings.Contains(err.Error(), GrokOAuthBaseURL) {
		t.Fatalf("AssembleConnect() error = %v", err)
	}
}

func TestGrok46OAuthDefaultsIncludeXHighReasoning(t *testing.T) {
	defaults, err := ResolveModelDefaults("grok", "grok-4.6")
	if err != nil {
		t.Fatal(err)
	}
	if defaults.ContextWindowTokens != 500000 || defaults.MaxOutputTokens != 32768 ||
		defaults.DefaultReasoningEffort != "high" ||
		strings.Join(defaults.ReasoningLevels, ",") != "low,medium,high,xhigh" {
		t.Fatalf("grok-4.6 defaults = %#v", defaults)
	}
	if defaults.ImageInput == nil || !*defaults.ImageInput {
		t.Fatalf("grok-4.6 image input = %v, want maintained true", defaults.ImageInput)
	}
}

func TestGrokOAuthNonReasoningModelDisablesReasoning(t *testing.T) {
	defaults, err := ResolveModelDefaults("grok", "grok-4.20-0309-non-reasoning")
	if err != nil {
		t.Fatal(err)
	}
	if defaults.ReasoningMode != "none" || defaults.DefaultReasoningEffort != "" || len(defaults.ReasoningLevels) != 0 {
		t.Fatalf("non-reasoning defaults = %#v", defaults)
	}
	if defaults.ImageInput == nil || !*defaults.ImageInput {
		t.Fatalf("non-reasoning image input = %v, want maintained true", defaults.ImageInput)
	}
}

func TestMaintainedSelectableGrokOAuthModels(t *testing.T) {
	models, err := MaintainedSelectableModels(context.Background(), "xai", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Name != "grok-4.6" || models[1].Name != "grok-4.5" {
		t.Fatalf("models = %#v, want grok-4.6 then grok-4.5", models)
	}
	for _, model := range models {
		if !model.MetadataComplete || !model.ImageInputKnown {
			t.Fatalf("model = %#v, want complete image-capable metadata", model)
		}
	}
}
