package configstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestStoreRejectsDuplicateCurrentRecordsBeforeNormalization(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(AppConfig) AppConfig
		wantErr string
	}{
		{
			name: "provider model",
			mutate: func(doc AppConfig) AppConfig {
				doc.Models.Configs = append(doc.Models.Configs, doc.Models.Configs[0])
				return doc
			},
			wantErr: "duplicate provider model",
		},
		{
			name: "conflicting provider endpoint fields",
			mutate: func(doc AppConfig) AppConfig {
				doc.Models.Configs[0].BaseURL = "https://conflicting.example/v1"
				return doc
			},
			wantErr: "conflicts with provider endpoint",
		},
		{
			name: "model profile",
			mutate: func(doc AppConfig) AppConfig {
				doc.ModelProfiles.Profiles = append(doc.ModelProfiles.Profiles, doc.ModelProfiles.Profiles[0])
				return doc
			},
			wantErr: "duplicate model profile",
		},
		{
			name: "Agent binding",
			mutate: func(doc AppConfig) AppConfig {
				doc.AgentBindings.Bindings = append(doc.AgentBindings.Bindings, doc.AgentBindings.Bindings[0])
				return doc
			},
			wantErr: "duplicate Agent binding for handle",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := test.mutate(currentValidationFixture())
			store := New(t.TempDir())
			if err := store.Save(doc); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Save() error = %v, want containing %q", err, test.wantErr)
			}

			root := t.TempDir()
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, "config.json"), data, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if _, err := New(root).Load(); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func currentValidationFixture() AppConfig {
	model := modelconfig.NormalizeConfig(modelconfig.Config{
		Provider:               "openai-codex",
		Model:                  "gpt-5.6",
		CredentialRef:          modelconfig.CodexOAuthCredentialRef,
		ReasoningMode:          "effort",
		ReasoningLevels:        []string{"low", "high"},
		DefaultReasoningEffort: "high",
	})
	endpoint := modelconfig.ProviderEndpointFromConfig(model)
	model = modelconfig.MergeConfigProviderEndpoint(model, endpoint)
	profile := modelprofile.ModelProfile{
		ID:          modelprofile.BuildProviderID(model.ID),
		DisplayName: model.ID,
		Backend: modelprofile.Backend{
			Provider: &modelprofile.ProviderBackend{ModelConfigID: model.ID},
		},
		Effort: modelprofile.EffortCapability{
			DefaultEffort: "high",
			Choices: []modelprofile.EffortChoice{
				{Canonical: "low", WireValue: "low"},
				{Canonical: "high", WireValue: "high"},
			},
		},
	}
	return AppConfig{
		SchemaVersion: SchemaVersionV2,
		Models: PersistedModelConfig{
			DefaultID:         model.ID,
			ProviderEndpoints: []modelconfig.ProviderEndpointConfig{endpoint},
			Configs:           []modelconfig.Config{model},
		},
		ModelProfiles: modelprofile.Configuration{
			DefaultProfileID: profile.ID,
			Profiles:         []modelprofile.ModelProfile{profile},
		},
		AgentBindings: agentbinding.Configuration{Bindings: []agentbinding.Binding{{
			Handle:    agentbinding.HandleOrbit,
			ProfileID: profile.ID,
			Effort:    "high",
		}}},
	}
}
