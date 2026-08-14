package controladapter

import "testing"

func TestConnectCatalogKeepsGrokProviderAndACPAgentSeparate(t *testing.T) {
	providers := completeConnectProviders("grok", 10)
	if len(providers) != 1 || providers[0].Value != "grok" || !providers[0].NoAuth {
		t.Fatalf("Grok provider candidates = %#v", providers)
	}
	agents := completeConnectACPAgents("grok", 10)
	if len(agents) != 1 || agents[0].Value != "grok" {
		t.Fatalf("Grok ACP candidates = %#v", agents)
	}
}
