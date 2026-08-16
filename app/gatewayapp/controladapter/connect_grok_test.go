package controladapter

import (
	"context"
	"strings"
	"testing"
)

func TestConnectCatalogKeepsGrokProviderAndACPAgentSeparate(t *testing.T) {
	providers := completeConnectProviders(context.Background(), nil, "grok", 10)
	if len(providers) != 1 || providers[0].Value != "grok" || !providers[0].NoAuth {
		t.Fatalf("Grok provider candidates = %#v", providers)
	}
	agents := completeConnectACPAgents("grok", 10)
	if len(agents) != 1 || agents[0].Value != "grok" {
		t.Fatalf("Grok ACP candidates = %#v", agents)
	}
}

func TestConnectCatalogMarksDefaultProviderCredentialReusable(t *testing.T) {
	t.Parallel()

	called := false
	driver := &assembler{stack: &RuntimeStack{Model: ModelRuntimeDeps{
		HasReusableAuthFn: func(_ context.Context, provider string, baseURL string) bool {
			called = true
			if provider != "deepseek" || baseURL != "https://api.deepseek.com/anthropic" {
				t.Fatalf("HasReusableAuthFn(%q, %q)", provider, baseURL)
			}
			return true
		},
	}}}

	providers := completeConnectProviders(context.Background(), driver, "deepseek", 10)
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
