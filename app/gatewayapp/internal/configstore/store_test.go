package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestAtomicWriteFileCommitBoundary(t *testing.T) {
	t.Parallel()

	for name, writeOps := range map[string]func(string, error) AtomicWriteOps{
		"destination chmod": func(path string, fault error) AtomicWriteOps {
			return AtomicWriteOps{Chmod: func(candidate string, mode os.FileMode) error {
				if candidate == path {
					return fault
				}
				return os.Chmod(candidate, mode)
			}}
		},
		"directory fsync": func(_ string, fault error) AtomicWriteOps {
			return AtomicWriteOps{FsyncDir: func(string) error { return fault }}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "config.json")
			original := []byte("old")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			fault := errors.New(name + " failed")
			err := AtomicWriteFile(path, []byte("new"), 0o600, writeOps(path, fault))
			if !errors.Is(err, fault) || !WriteCommitted(err) {
				t.Fatalf("AtomicWriteFile() error = %v, want committed %v", err, fault)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != "new" {
				t.Fatalf("destination = %q, want committed content", got)
			}
		})
	}

	t.Run("rename", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		fault := errors.New("rename failed")
		err := AtomicWriteFile(path, []byte("new"), 0o600, AtomicWriteOps{
			Rename: func(string, string) error { return fault },
		})
		if !errors.Is(err, fault) || WriteCommitted(err) {
			t.Fatalf("AtomicWriteFile() error = %v, want uncommitted %v", err, fault)
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != "old" {
			t.Fatalf("destination = %q, want original content", got)
		}
	})
}

func TestStorePersistsManagedCredentialReferenceWithoutCredentialMaterial(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := New(root)
	model := modelconfig.NormalizeConfig(modelconfig.Config{
		Provider:      "openai-codex",
		API:           modelconfig.DefaultAPIForProvider("openai-codex"),
		Model:         "gpt-5.5",
		BaseURL:       modelconfig.CodexOAuthBaseURL,
		CredentialRef: modelconfig.CodexOAuthCredentialRef,
	})
	profile := testProviderProfile(model, "none")
	if err := store.Save(AppConfig{
		Models:        PersistedModelConfig{DefaultID: model.ID, Configs: []modelconfig.Config{model}},
		ModelProfiles: modelprofile.Configuration{DefaultProfileID: profile.ID, Profiles: []modelprofile.ModelProfile{profile}},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatalf("ReadFile(config.json) error = %v", err)
	}
	if !strings.Contains(string(raw), `"credential_ref": "codex:default"`) || !strings.Contains(string(raw), `"provider_endpoints"`) {
		t.Fatalf("persisted config = %s", raw)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Models.ProviderEndpoints) != 1 || loaded.Models.ProviderEndpoints[0].CredentialRef != modelconfig.CodexOAuthCredentialRef || loaded.Models.ProviderEndpoints[0].Token != "" {
		t.Fatalf("loaded managed provider endpoint = %#v", loaded.Models.ProviderEndpoints)
	}
}

func TestStoreRejectsCredentialMaterialEvenAlongsideOpaqueReference(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*modelconfig.Config){
		"token":        func(configured *modelconfig.Config) { configured.Token = "must-not-persist" },
		"environment":  func(configured *modelconfig.Config) { configured.TokenEnv = "SECRET_ENV" },
		"persist flag": func(configured *modelconfig.Config) { configured.PersistToken = true },
	} {
		t.Run(name, func(t *testing.T) {
			configured := modelconfig.Config{
				Provider: "deepseek", Model: "reasoner", CredentialRef: "apikey:deepseek:test",
			}
			mutate(&configured)
			err := New(t.TempDir()).Save(AppConfig{Models: PersistedModelConfig{Configs: []modelconfig.Config{configured}}})
			if err == nil || !strings.Contains(err.Error(), "credential store") {
				t.Fatalf("Save() error = %v, want credential-store boundary", err)
			}
		})
	}
}

func TestStorePersistsExternalAgentAndACPModelProfile(t *testing.T) {
	t.Parallel()

	store := New(t.TempDir())
	profile := modelprofile.ModelProfile{
		ID: "acp:claude:opus", DisplayName: "Claude Opus",
		Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{
			AgentID: "claude", RemoteModelID: "claude-opus-4-8", SessionDefaults: map[string]string{"mode": "code"},
		}},
		Effort: modelprofile.EffortCapability{DefaultEffort: "xhigh", ACPConfigID: "effort", Choices: []modelprofile.EffortChoice{{Canonical: "xhigh", WireValue: "max"}}},
	}
	doc := AppConfig{ExternalAgents: controlagents.Configuration{
		Connections: []controlagents.Connection{{
			ID: "claude",
			Launcher: controlagents.Launcher{
				Kind:    controlagents.LaunchKindPackageExec,
				Command: "npx",
				Args:    []string{"-y", "claude-agent-acp"},
			},
		}},
		Agents: []controlagents.Agent{{
			ID:           "claude",
			ConnectionID: "claude",
		}},
	}, ModelProfiles: modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{profile}}}
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	agent, connection, err := controlagents.ResolveAgent(loaded.ExternalAgents, "claude")
	if err != nil {
		t.Fatalf("ResolveAgent() error = %v", err)
	}
	loadedProfile, ok := modelprofile.Lookup(loaded.ModelProfiles, profile.ID)
	if connection.Launcher.Command != "npx" || agent.ConnectionID != connection.ID || !ok || loadedProfile.Backend.ACP.SessionDefaults["mode"] != "code" {
		t.Fatalf("loaded external Agent/profile = %#v %#v %#v", agent, connection, loadedProfile)
	}
}

func TestStorePersistsUnifiedBindingAndRejectsStaleProfileReference(t *testing.T) {
	t.Parallel()

	store := New(t.TempDir())
	model := modelconfig.NormalizeConfig(modelconfig.Config{
		Provider:        "openai-codex",
		Model:           "gpt-5.6-luna",
		ReasoningMode:   "effort",
		ReasoningLevels: []string{"low", "medium", "high", "xhigh"},
	})
	profile := testProviderProfile(model, "high")
	bindings, err := agentbinding.Bind(agentbinding.Configuration{}, agentbinding.Binding{
		Handle: agentbinding.HandleOrbit, ProfileID: profile.ID, Effort: "high",
	}, modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{profile}})
	if err != nil {
		t.Fatalf("BindAgent() error = %v", err)
	}
	doc := AppConfig{
		Models:        PersistedModelConfig{DefaultID: model.ID, Configs: []modelconfig.Config{model}},
		ModelProfiles: modelprofile.Configuration{DefaultProfileID: profile.ID, Profiles: []modelprofile.ModelProfile{profile}},
		AgentBindings: bindings,
	}
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, ok := agentbinding.Lookup(loaded.AgentBindings, agentbinding.HandleOrbit)
	if !ok || resolved.ProfileID != profile.ID || resolved.Effort != "high" {
		t.Fatalf("loaded binding = %#v, %v", resolved, ok)
	}

	doc.ModelProfiles = modelprofile.Configuration{}
	if err := store.Save(doc); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("Save(stale binding) error = %v, want unknown profile", err)
	}
}

func TestStoreSetPathConcurrentWithLoadSave(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := New(root)
	if store == nil {
		t.Fatal("New() = nil")
	}
	paths := []string{
		filepath.Join(root, "one", "config.json"),
		filepath.Join(root, "two", "config.json"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var setters sync.WaitGroup
	for i := 0; i < 4; i++ {
		setters.Add(1)
		go func(offset int) {
			defer setters.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					store.SetPath(paths[offset%len(paths)])
					_ = store.Path()
				}
			}
		}(i)
	}
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				if _, err := store.Load(); err != nil {
					t.Errorf("Load() error = %v", err)
					cancel()
					return
				}
				if err := store.Save(AppConfig{}); err != nil {
					t.Errorf("Save() error = %v", err)
					cancel()
					return
				}
			}
		}()
	}
	workers.Wait()
	cancel()
	setters.Wait()
}

func testProviderProfile(model modelconfig.Config, effort string) modelprofile.ModelProfile {
	choices := []modelprofile.EffortChoice{{Canonical: effort, WireValue: effort}}
	if effort == "none" {
		choices[0].WireValue = "none"
	}
	return modelprofile.ModelProfile{
		ID: modelprofile.BuildProviderID(model.ID), DisplayName: model.ID,
		Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: model.ID}},
		Effort:  modelprofile.EffortCapability{DefaultEffort: effort, Choices: choices},
	}
}
