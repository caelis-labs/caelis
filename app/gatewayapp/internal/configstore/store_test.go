package configstore

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

func TestStorePersistsUserAgentRoster(t *testing.T) {
	t.Parallel()

	store := New(t.TempDir())
	doc := AppConfig{AgentRoster: controlagents.Configuration{
		Connections: []controlagents.Connection{{
			ID: "claude",
			Launcher: controlagents.Launcher{
				Kind:    controlagents.LaunchKindPackageExec,
				Command: "npx",
				Args:    []string{"-y", "claude-agent-acp"},
			},
		}},
		Agents: []controlagents.Agent{{
			ID:      "opus",
			Backing: controlagents.AgentBacking{ConnectionID: "claude"},
			Defaults: controlagents.SessionOptions{
				ModelID:      "claude-opus-4-8",
				ConfigValues: map[string]string{"effort": "max"},
			},
		}},
	}}
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	agent, connection, err := controlagents.ResolveAgent(loaded.AgentRoster, "opus")
	if err != nil {
		t.Fatalf("ResolveAgent() error = %v", err)
	}
	if connection.Launcher.Command != "npx" || agent.Defaults.ModelID != "claude-opus-4-8" || agent.Defaults.ConfigValues["effort"] != "max" {
		t.Fatalf("loaded roster placement = %#v %#v", agent, connection)
	}
}

func TestStorePersistsModelBackedAgentAndRejectsStaleModelReference(t *testing.T) {
	t.Parallel()

	store := New(t.TempDir())
	model := modelconfig.NormalizeConfig(modelconfig.Config{Provider: "ollama", Model: "deepseek-v4-pro"})
	doc := AppConfig{
		Models: PersistedModelConfig{DefaultID: model.ID, Configs: []modelconfig.Config{model}},
		AgentRoster: controlagents.Configuration{Agents: []controlagents.Agent{{
			ID: "deepseek-v4-pro", Backing: controlagents.AgentBacking{ModelAlias: model.ID},
		}}},
	}
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	agent, ok := controlagents.LookupAgent(loaded.AgentRoster, "deepseek-v4-pro")
	if !ok || agent.Backing.ModelAlias != model.ID {
		t.Fatalf("loaded model-backed Agent = %#v, %v", agent, ok)
	}

	doc.Models.Configs = nil
	if err := store.Save(doc); err == nil {
		t.Fatal("Save(stale model Agent) error = nil, want unknown configured model rejection")
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
