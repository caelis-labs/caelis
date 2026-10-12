package gatewayapp

import (
	"testing"

	"github.com/caelis-labs/caelis/control/modelconfig"
)

func TestReloadedSol61ConfigHydratesOnlyMissingContextWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		saved  int
		window int
	}{
		{name: "missing", window: 475000},
		{name: "previous usable default", saved: 258400, window: 258400},
		{name: "previous raw default", saved: 272000, window: 272000},
		{name: "explicit raw window", saved: 500000, window: 500000},
		{name: "explicit long window", saved: 1000000, window: 1000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := modelconfig.NormalizeConfig(ModelConfig{
				Provider:            "openai-codex",
				Model:               "gpt-6.1-sol",
				BaseURL:             modelconfig.CodexOAuthBaseURL,
				CredentialRef:       modelconfig.CodexOAuthCredentialRef,
				ContextWindowTokens: tc.saved,
				MaxOutputTok:        32768,
			})
			store := newAppConfigStore(t.TempDir())
			if err := store.Save(AppConfig{Models: persistedModelConfig{Configs: []ModelConfig{cfg}}}); err != nil {
				t.Fatal(err)
			}
			lookup, err := newModelLookup(store, 128000)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := lookup.ResolveConfig(cfg.Alias)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Model != cfg.Model || resolved.ContextWindowTokens != tc.window || !modelconfig.ModelSupportsImages(resolved) {
				t.Fatalf("reloaded config = %#v, want preserved identity/image support and window %d", resolved, tc.window)
			}
			doc, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Models.Configs) != 1 || doc.Models.Configs[0].ContextWindowTokens != tc.saved {
				t.Fatalf("hydration rewrote persisted window: %#v", doc.Models.Configs)
			}
		})
	}
}
