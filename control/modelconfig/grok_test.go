package modelconfig

import (
	"context"
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
	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "grok",
		Models: []ModelSelection{
			{Name: "grok-4.5"},
			{Name: "grok-4.20", ReasoningEffort: "medium"},
		},
	}, ConnectOptions{
		Authenticate: func(_ context.Context, req AuthenticateRequest) (AuthenticateResult, error) {
			authCalls++
			if req.Provider != "xai" || req.BaseURL != GrokOAuthBaseURL || req.Purpose != AuthPurposeConnect {
				t.Fatalf("authenticate request = %#v", req)
			}
			return AuthenticateResult{}, nil
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
		if cfg.Token != "" || cfg.TokenEnv != "" || cfg.PersistToken {
			t.Fatalf("credential material leaked into config = %#v", cfg)
		}
	}
	if got := configs[0]; got.ContextWindowTokens != 500000 || got.MaxOutputTok != 32768 ||
		got.ReasoningEffort != "high" || strings.Join(got.ReasoningLevels, ",") != "low,medium,high" {
		t.Fatalf("grok-4.5 defaults = %#v", got)
	}
}

func TestAssembleConnectRejectsCustomGrokOAuthEndpoint(t *testing.T) {
	_, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "grok",
		BaseURL:  "https://proxy.example/v1",
		Models:   []ModelSelection{{Name: "grok-4.5"}},
	}, ConnectOptions{Authenticate: func(context.Context, AuthenticateRequest) (AuthenticateResult, error) {
		t.Fatal("authentication ran before endpoint validation")
		return AuthenticateResult{}, nil
	}})
	if err == nil || !strings.Contains(err.Error(), GrokOAuthBaseURL) {
		t.Fatalf("AssembleConnect() error = %v", err)
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
}

func TestSelectableGrokOAuthModelsUseRemoteCatalogOrFallback(t *testing.T) {
	t.Run("remote", func(t *testing.T) {
		models, err := SelectableModels(context.Background(), "grok", "", func(context.Context, AuthenticateRequest) (AuthenticateResult, error) {
			return AuthenticateResult{
				SelectableModels:          []string{"grok-4.20", "grok-imagine-image", "GROK-4.5", "other", "grok-4.20"},
				ModelCatalogAuthoritative: true,
			}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(models) != 2 || models[0].Name != "grok-4.20" || models[1].Name != "grok-4.5" {
			t.Fatalf("models = %#v", models)
		}
		for _, entry := range models {
			if !entry.MetadataComplete {
				t.Fatalf("metadata incomplete for %#v", entry)
			}
		}
	})

	t.Run("fallback", func(t *testing.T) {
		models, err := SelectableModels(context.Background(), "xai", "", func(context.Context, AuthenticateRequest) (AuthenticateResult, error) {
			return AuthenticateResult{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(models) != 1 || models[0].Name != "grok-4.5" || !models[0].MetadataComplete {
			t.Fatalf("models = %#v", models)
		}
	})
}
