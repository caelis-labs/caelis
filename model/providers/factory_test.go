package providers

import (
	"context"
	"testing"

	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/model/catalog"
)

func TestConfiguredFactoryResolvesMigratedOpenAICompatibleProviders(t *testing.T) {
	factory, err := NewConfiguredFactory([]ModelConfig{
		{Provider: "openai", Model: "gpt-test", Token: "openai-token"},
		{Provider: "openai-compatible", Model: "compat-test", Token: "compat-token", BaseURL: "https://compat.example/v1"},
		{Provider: "deepseek", Model: "deepseek-v4-pro", Token: "deepseek-token"},
		{Provider: "openrouter", Model: "openrouter/openai/gpt-4o-mini", Token: "openrouter-token"},
		{Provider: "mimo", Model: "mimo-v2-flash", Token: "mimo-token"},
		{Provider: "xiaomi", Model: "mimo-v2-pro", Token: "xiaomi-token"},
		{Provider: "volcengine", Model: "doubao-seed-2.0-pro", Token: "volc-token"},
		{Provider: "volcengine_coding_plan", Model: "doubao-seed-2.0-pro", Token: "coding-token"},
	})
	if err != nil {
		t.Fatalf("NewConfiguredFactory() error = %v", err)
	}

	tests := []struct {
		modelID  string
		provider string
		baseURL  string
	}{
		{"openai/gpt-test", "openai", defaultOpenAIBaseURL},
		{"openai-compatible/compat-test", "openai-compatible", "https://compat.example/v1"},
		{"deepseek/deepseek-v4-pro", "deepseek", defaultDeepSeekBaseURL},
		{"openrouter/openai/gpt-4o-mini", "openrouter", defaultOpenRouterBaseURL},
		{"mimo/mimo-v2-flash", "mimo", defaultMimoBaseURL},
		{"xiaomi/mimo-v2-pro", "mimo", defaultMimoBaseURL},
		{"volcengine/doubao-seed-2.0-pro", "volcengine", defaultVolcengineBaseURL},
		{"volcengine_coding_plan/doubao-seed-2.0-pro", "volcengine-coding", defaultVolcengineCodingBaseURL},
	}
	for _, tc := range tests {
		t.Run(tc.modelID, func(t *testing.T) {
			llm, err := factory.New(model.Ref{ModelID: tc.modelID})
			if err != nil {
				t.Fatalf("New(%q) error = %v", tc.modelID, err)
			}
			provider, ok := llm.(*OpenAIProvider)
			if !ok {
				t.Fatalf("llm type = %T, want *OpenAIProvider", llm)
			}
			if provider.provider != tc.provider {
				t.Fatalf("provider = %q, want %q", provider.provider, tc.provider)
			}
			if provider.baseURL != tc.baseURL {
				t.Fatalf("baseURL = %q, want %q", provider.baseURL, tc.baseURL)
			}
		})
	}
}

func TestConfiguredFactoryModelInfosBackCatalogRegistry(t *testing.T) {
	factory, err := NewConfiguredFactory([]ModelConfig{
		{
			Alias:         "fast",
			Provider:      "deepseek",
			Model:         "deepseek-v4-flash",
			Token:         "token",
			MaxTokens:     1048576,
			SupportsTools: true,
		},
	})
	if err != nil {
		t.Fatalf("NewConfiguredFactory() error = %v", err)
	}
	registry := catalog.New(catalog.Config{
		Models:  factory.ModelInfos(),
		Factory: factory.New,
	})

	llm, info, err := registry.Resolve(context.Background(), model.Ref{Alias: "fast"})
	if err != nil {
		t.Fatalf("Resolve(alias) error = %v", err)
	}
	if llm == nil {
		t.Fatal("Resolve(alias) llm = nil")
	}
	if info.ModelID != "deepseek/deepseek-v4-flash" || info.Provider != "deepseek" || info.MaxTokens != 1048576 || !info.SupportsTools {
		t.Fatalf("info = %#v, want deepseek model metadata", info)
	}
}

func TestConfiguredFactoryRejectsUnsupportedProvider(t *testing.T) {
	_, err := NewConfiguredFactory([]ModelConfig{{Provider: "unknown", Model: "test"}})
	if err == nil {
		t.Fatal("NewConfiguredFactory() error = nil, want unsupported provider")
	}
}

func TestConfiguredFactoryResolvesCodeFree(t *testing.T) {
	factory, err := NewConfiguredFactory([]ModelConfig{{
		Provider:           "codefree",
		Model:              "GLM-5.1",
		CodeFreeCredential: CodeFreeCredentials{UserID: "272182", APIKey: "api-key"},
	}})
	if err != nil {
		t.Fatalf("NewConfiguredFactory() error = %v", err)
	}
	llm, err := factory.New(model.Ref{ModelID: "codefree/GLM-5.1"})
	if err != nil {
		t.Fatalf("factory.New() error = %v", err)
	}
	provider, ok := llm.(*CodeFreeProvider)
	if !ok {
		t.Fatalf("llm type = %T, want *CodeFreeProvider", llm)
	}
	if provider.credentials.UserID != "272182" || provider.credentials.APIKey != "api-key" {
		t.Fatalf("credentials = %#v, want configured credentials", provider.credentials)
	}
}
