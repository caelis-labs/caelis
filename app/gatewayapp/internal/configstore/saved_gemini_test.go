package configstore

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

func TestSavedGeminiImageCapabilitySurvivesLoadAndSave(t *testing.T) {
	for _, name := range []string{"gemini-pro-latest", "gemini-3.1-pro"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			id := "gemini@default/gemini/" + name
			// The pre-pinned writer omitted API, auth type and image_input.
			wire := fmt.Sprintf(`{"schema_version":2,"models":{"provider_endpoints":[{"id":"gemini@default","provider":"gemini","base_url":"https://generativelanguage.googleapis.com/v1beta","credential_ref":"apikey:fixture:gemini"}],"configs":[{"id":%q,"alias":%q,"provider_endpoint_id":"gemini@default","model":%q,"context_window_tokens":1000000}]},"model_profiles":{"default_profile_id":%q,"default_effort":"none","profiles":[{"id":%q,"display_name":%q,"backend":{"provider":{"model_config_id":%q}},"effort":{"default_effort":"none","choices":[{"canonical":"none","wire_value":"none"}]}}]}}`, id, "gemini/"+name, name, "provider:"+id, "provider:"+id, name, id)
			if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(wire), 0600); err != nil {
				t.Fatal(err)
			}
			store := New(root)
			for range 2 {
				doc, err := store.Load()
				if err != nil {
					t.Fatal(err)
				}
				cfg := modelconfig.MergeConfigProviderEndpoint(doc.Models.Configs[0], doc.Models.ProviderEndpoints[0])
				if cfg.ID != id || cfg.CredentialRef != "apikey:fixture:gemini" || cfg.ContextWindowTokens != 1000000 || doc.ModelProfiles.DefaultProfileID != "provider:"+id {
					t.Fatalf("saved identity or settings changed: %#v", cfg)
				}
				cfg.Token = "synthetic"
				resolved, err := modelconfig.BuildModel(cfg, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				image := model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: "aW1n"}, "image/png", "fixture.png")
				request := &model.Request{Messages: []model.Message{model.NewMessage(model.RoleUser, image)}}
				if err := model.ValidateRequestCapabilities(resolved.Model, request); err != nil {
					t.Fatalf("saved Gemini image request rejected: %v", err)
				}
				if err := store.Save(doc); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
