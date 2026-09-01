package configstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/mcpconfig"
	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/workspacetrust"
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
		{
			name: "custom role",
			mutate: func(doc AppConfig) AppConfig {
				doc.AgentBindings.Roles = append(doc.AgentBindings.Roles, doc.AgentBindings.Roles[0])
				return doc
			},
			wantErr: "duplicate custom role",
		},
		{
			name: "incomplete custom role",
			mutate: func(doc AppConfig) AppConfig {
				doc.AgentBindings.Roles = append(doc.AgentBindings.Roles, agentbinding.Role{Handle: "draft"})
				return doc
			},
			wantErr: "requires a description",
		},
		{
			name: "binding set",
			mutate: func(doc AppConfig) AppConfig {
				doc.AgentBindings.Sets = append(doc.AgentBindings.Sets, doc.AgentBindings.Sets[0])
				return doc
			},
			wantErr: "duplicate binding set",
		},
		{
			name: "incomplete binding set",
			mutate: func(doc AppConfig) AppConfig {
				doc.AgentBindings.Sets[0].Bindings = append(
					doc.AgentBindings.Sets[0].Bindings,
					agentbinding.Binding{Handle: "research", ProfileID: doc.ModelProfiles.DefaultProfileID},
				)
				return doc
			},
			wantErr: "contains an incomplete binding",
		},
		{
			name: "MCP server",
			mutate: func(doc AppConfig) AppConfig {
				doc.MCPServers = mcpconfig.Servers{
					"docs": {},
				}
				return doc
			},
			wantErr: "invalid MCP servers",
		},
		{
			name: "MCP server identity",
			mutate: func(doc AppConfig) AppConfig {
				doc.MCPServers = mcpconfig.Servers{
					"docs":   {Command: "npx"},
					" docs ": {Command: "other"},
				}
				return doc
			},
			wantErr: "duplicate MCP server",
		},
		{
			name: "Memory Bot",
			mutate: func(doc AppConfig) AppConfig {
				doc.Memory = currentMemoryBindingFixture()
				duplicate := doc.Memory.Bots[0]
				duplicate.RuntimeActorRef = "actor-b"
				doc.Memory.Bots = append(doc.Memory.Bots, duplicate)
				return doc
			},
			wantErr: "duplicate Bot ID",
		},
		{
			name: "Memory Runtime actor",
			mutate: func(doc AppConfig) AppConfig {
				doc.Memory = currentMemoryBindingFixture()
				duplicate := doc.Memory.Bots[0]
				duplicate.BotID = "bot-b"
				doc.Memory.Bots = append(doc.Memory.Bots, duplicate)
				return doc
			},
			wantErr: "duplicate Runtime actor",
		},
		{
			name: "workspace trust level",
			mutate: func(doc AppConfig) AppConfig {
				doc.WorkspaceTrust = workspacetrust.Configuration{
					filepath.Join(string(filepath.Separator), "workspace"): workspacetrust.Unknown,
				}
				return doc
			},
			wantErr: "invalid workspace trust",
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

func TestStoreRoundTripsDisabledMemoryBindingWithoutSecretMaterial(t *testing.T) {
	store := New(t.TempDir())
	doc := currentValidationFixture()
	doc.Memory = currentMemoryBindingFixture()
	doc.Memory.Enabled = false
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := memorybinding.Normalize(doc.Memory)
	if got := loaded.Memory; !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded Memory binding = %#v, want %#v", got, want)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("issuer-secret")) || !bytes.Contains(raw, []byte(`"issuer_credential_ref"`)) {
		t.Fatalf("persisted Memory binding contains secret or omits credential reference: %s", raw)
	}
}

func TestZeroMemoryBindingIsOmittedFromCurrentDocument(t *testing.T) {
	store := New(t.TempDir())
	if err := store.Save(currentValidationFixture()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"memory"`)) {
		t.Fatalf("zero Memory binding was persisted: %s", raw)
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
		AgentBindings: agentbinding.Configuration{
			Roles: []agentbinding.Role{{
				Handle: "research", Description: "Investigate unfamiliar systems.",
			}},
			Bindings: []agentbinding.Binding{{
				Handle:    agentbinding.HandleOrbit,
				ProfileID: profile.ID,
				Effort:    "high",
			}},
			Sets: []agentbinding.BindingSet{{
				Name: "baseline",
				Bindings: []agentbinding.Binding{{
					Handle:    agentbinding.HandleOrbit,
					ProfileID: profile.ID,
					Effort:    "high",
				}},
			}},
		},
	}
}

func currentMemoryBindingFixture() memorybinding.Configuration {
	return memorybinding.Configuration{
		Enabled: true,
		Endpoint: memorybinding.EndpointConfig{
			ID: "memory-default", Deployment: memorybinding.DeploymentModeManagedLocal,
			Compatibility: memorybinding.APICompatibility{
				Protocol: "memory.local.v1alpha1", APIVersion: "memory.v1alpha1",
				CoreProfile: "memory.core.v1alpha1", ServiceVersion: "0.2.0-alpha.1",
				BuildRevision: strings.Repeat("a", 40), ArtifactSHA256: strings.Repeat("b", 64),
			},
		},
		Bots: []memorybinding.BotMemoryBinding{{
			BotID: "bot-a", RuntimeActorRef: "actor-a", MemoryIdentityRef: "identity-a",
			PrincipalRef: "principal:a", IssuerCredentialRef: "memory-issuer:bot-a",
			Private:        memorybinding.AudienceBinding{ViewRef: "view-a", GrantRef: "grant-a"},
			BindingVersion: 1,
		}},
	}
}
