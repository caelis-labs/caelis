package plugin

import "testing"

func TestStateCloneDetachesMarketplaceDependencyLists(t *testing.T) {
	t.Parallel()

	original := State{
		Plugins: []Config{{ID: "demo"}},
		Marketplaces: []MarketplaceConfig{{
			Name:                              "market",
			AllowCrossMarketplaceDependencies: []string{"shared"},
		}},
	}
	cloned := original.Clone()
	cloned.Plugins[0].ID = "changed"
	cloned.Marketplaces[0].Name = "changed"
	cloned.Marketplaces[0].AllowCrossMarketplaceDependencies[0] = "changed"

	if original.Plugins[0].ID != "demo" {
		t.Fatalf("original Plugins = %#v", original.Plugins)
	}
	if original.Marketplaces[0].Name != "market" ||
		original.Marketplaces[0].AllowCrossMarketplaceDependencies[0] != "shared" {
		t.Fatalf("original Marketplaces = %#v", original.Marketplaces)
	}
}

func TestNormalizeConfigUsesManifestKind(t *testing.T) {
	t.Parallel()

	got := NormalizeConfig(Config{ID: " Demo ", Kind: ManifestKind(" CLAUDE ")})
	if got.ID != "demo" || got.Kind != ManifestKindClaude {
		t.Fatalf("NormalizeConfig() = %#v", got)
	}
}
