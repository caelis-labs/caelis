package configstore

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/modelprofile/builder"
)

func TestSavedMagpieLabelsRefreshWithoutChangingSelectionOrBackend(t *testing.T) {
	cfg := modelconfig.NormalizeConfig(modelconfig.Config{Provider: "magpie", Model: "codex/gpt-6-luna", BaseURL: "http://127.0.0.1:3425/v1", CredentialRef: "apikey:fixture"})
	profile, err := builder.FromProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	profile.DisplayName = cfg.Alias
	doc := AppConfig{ModelProfiles: modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{profile}, DefaultProfileID: profile.ID, DefaultEffort: "none"}}
	doc.Models.Configs = []modelconfig.Config{cfg}
	doc.Models.ProviderEndpoints = []modelconfig.ProviderEndpointConfig{modelconfig.ProviderEndpointFromConfig(cfg)}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var saved AppConfig
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	saved = Normalize(saved)
	p := saved.ModelProfiles.Profiles[0]
	if p.DisplayName != "codex/gpt-6-luna" || p.ID != profile.ID || p.Backend.Provider.ModelConfigID != cfg.ID || saved.ModelProfiles.DefaultProfileID != profile.ID {
		t.Fatalf("saved selection changed: %#v", saved.ModelProfiles)
	}
	if saved.Models.Configs[0].Alias != cfg.Alias || saved.Models.ProviderEndpoints[0].CredentialRef != cfg.CredentialRef {
		t.Fatal("labels changed aliases or credential references")
	}
}
