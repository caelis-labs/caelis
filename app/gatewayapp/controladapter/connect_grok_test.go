package controladapter

import (
	"context"
	"strings"
	"testing"
)

func TestConnectCatalogKeepsGrokProviderAndACPAgentSeparate(t *testing.T) {
	providers := completeConnectProviders(context.Background(), nil, "account", "grok", 10)
	if len(providers) != 1 || providers[0].Value != "grok" || !providers[0].NoAuth {
		t.Fatalf("Grok provider candidates = %#v", providers)
	}
	agents := completeConnectACPAgents("grok", 10)
	if len(agents) != 1 || agents[0].Value != "grok" {
		t.Fatalf("Grok ACP candidates = %#v", agents)
	}
}

func TestConnectCatalogSeparatesAccountAndAPIKeyProviders(t *testing.T) {
	account := completeConnectProviders(context.Background(), nil, "account", "", 100)
	for _, want := range []string{"codex", "grok"} {
		if !slashCandidatesHaveValue(account, want) {
			t.Fatalf("account providers = %#v, want %q", account, want)
		}
	}
	if slashCandidatesHaveValue(account, "ollama") {
		t.Fatalf("account providers = %#v, should not include Ollama", account)
	}

	apiKey := completeConnectProviders(context.Background(), nil, "api-key", "", 100)
	if !slashCandidatesHaveValue(apiKey, "ollama") {
		t.Fatalf("API-key providers = %#v, want Ollama", apiKey)
	}
	for _, hidden := range []string{"codex", "grok"} {
		if slashCandidatesHaveValue(apiKey, hidden) {
			t.Fatalf("API-key providers = %#v, should not include account provider %q", apiKey, hidden)
		}
	}
	legacy, err := completeConnectArgs(context.Background(), nil, "connect-provider", "", 100)
	if err != nil || !slashCandidatesHaveValue(legacy, "codex") || !slashCandidatesHaveValue(legacy, "ollama") {
		t.Fatalf("legacy provider catalog = %#v, err=%v, want account and API-key providers", legacy, err)
	}

	sources := completeConnectSources("", 10)
	for _, want := range []string{"account", "api-key", "acp"} {
		if !slashCandidatesHaveValue(sources, want) {
			t.Fatalf("connect sources = %#v, want %q", sources, want)
		}
	}
	if slashCandidatesHaveValue(sources, "disconnect") {
		t.Fatalf("connect sources = %#v, should not include disconnect", sources)
	}
}

func TestConnectCatalogMarksDefaultProviderCredentialReusable(t *testing.T) {
	t.Parallel()

	called := false
	driver := &assembler{deps: &runtimeDeps{Model: ModelRuntimeDeps{
		HasReusableAuthFn: func(_ context.Context, provider string, baseURL string) bool {
			called = true
			if provider != "deepseek" || baseURL != "https://api.deepseek.com/anthropic" {
				t.Fatalf("HasReusableAuthFn(%q, %q)", provider, baseURL)
			}
			return true
		},
	}}}

	providers := completeConnectProviders(context.Background(), driver, "api-key", "deepseek", 10)
	if !called {
		t.Fatal("HasReusableAuthFn was not called")
	}
	if len(providers) != 1 || providers[0].Value != "deepseek" || !providers[0].NoAuth {
		t.Fatalf("DeepSeek provider candidates = %#v, want reusable auth", providers)
	}
	if !strings.Contains(providers[0].Detail, "configured auth") {
		t.Fatalf("DeepSeek provider detail = %q, want configured auth", providers[0].Detail)
	}
}
