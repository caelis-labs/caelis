package modelconfig

import "testing"

func TestRetiredProvidersRejectNewConnections(t *testing.T) {
	for _, provider := range []string{"openai", "openai-compatible", "openai-chat-compatible", "openai-responses-compatible", "anthropic", "anthropic-compatible", "gemini", "openrouter", "typesafe", "minimax", "volcengine"} {
		t.Run(provider, func(t *testing.T) {
			if _, ok := LookupProvider(provider); ok {
				t.Fatal("retired provider still resolves for onboarding")
			}
			if _, err := AssembleConnect(t.Context(), ConnectRequest{Provider: provider, APIKey: "fixture", BaseURL: "https://provider.invalid", Models: []ModelSelection{{Name: "fixture"}}}, ConnectOptions{}); err == nil {
				t.Fatal("retired provider accepted a new connection")
			}
			if models, err := SelectableModels(t.Context(), ConnectRequest{Provider: provider}); err != nil || len(models) != 0 {
				t.Fatalf("retired directory: %#v %v", models, err)
			}
		})
	}
}

func TestSavedEndpointUsesExplicitOrLegacyProtocol(t *testing.T) {
	endpoint := NormalizeProviderEndpoint(ProviderEndpointConfig{ID: "volcengine@coding-plan", Provider: "volcengine", BaseURL: "https://ark.cn-beijing.volces.com/api/coding/v3"})
	if endpoint.API != "volcengine_coding_plan" || endpoint.EndpointID != "coding-plan" || endpoint.ID != "volcengine@coding-plan" {
		t.Fatalf("saved endpoint = %#v", endpoint)
	}
	endpoint.API = "openai_compatible"
	if got := NormalizeProviderEndpoint(endpoint); got.API != endpoint.API || got.ID != endpoint.ID {
		t.Fatalf("explicit protocol overwritten: %#v", got)
	}
}
