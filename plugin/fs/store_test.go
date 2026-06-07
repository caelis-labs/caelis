package fs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
	pluginfs "github.com/OnslaughtSnail/caelis/plugin/fs"
)

func TestStoreInstallWritesLockAndRegistryLoadsPlugin(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, ".codex-plugin", "plugin.json"), `{
		"name": "superpowers",
		"version": "5.1.0",
		"skills": "./skills/",
		"mcpServers": [{
			"name": "memory",
			"transport": "stdio",
			"command": "node",
			"args": ["server.js"]
		}],
		"contributions": {
			"agents": [{"name": "reviewer", "command": "caelis", "args": ["acp"]}],
			"modes": [{"id": "safe", "name": "Safe"}],
			"configs": [{"id": "verbosity", "defaultValue": "low"}],
			"systemPrompt": "Use installed plugin guidance.",
			"policyMode": "workspace-write",
			"extraReadRoots": ["./docs"],
			"extraWriteRoots": ["./generated"]
		}
	}`)
	mustWrite(t, filepath.Join(repo, "skills", "brainstorming", "SKILL.md"), `---
name: brainstorming
description: Refine ideas before implementation.
---
# Brainstorming
`)

	ctx := context.Background()
	resolver := pluginfs.NewResolver()
	resolved, err := resolver.Resolve(ctx, caelisplugin.ResolveRequest{Root: repo})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	storeRoot := t.TempDir()
	store := pluginfs.NewStore(pluginfs.StoreConfig{Root: storeRoot})
	installed, err := store.Install(ctx, resolved)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if installed.Name != "superpowers" || installed.Version != "5.1.0" {
		t.Fatalf("installed = %#v, want superpowers/5.1.0", installed)
	}
	if len(installed.MCPServers) != 1 || installed.MCPServers[0].Name != "memory" {
		t.Fatalf("installed mcp servers = %#v, want memory", installed.MCPServers)
	}
	if installed.SystemPrompt != "Use installed plugin guidance." || installed.PolicyMode != "workspace-write" {
		t.Fatalf("installed prompt/policy = %#v", installed)
	}
	if len(installed.Agents) != 1 || installed.Agents[0].Name != "reviewer" {
		t.Fatalf("installed agents = %#v", installed.Agents)
	}

	if _, err := os.Stat(filepath.Join(storeRoot, "plugins.lock.json")); err != nil {
		t.Fatalf("lock file stat error = %v", err)
	}
	installedList, err := store.List(ctx)
	if err != nil {
		t.Fatalf("store List() error = %v", err)
	}
	if len(installedList) != 1 || len(installedList[0].MCPServers) != 1 || installedList[0].MCPServers[0].Command != "node" {
		t.Fatalf("stored plugins = %#v, want mcp server in lock", installedList)
	}
	if installedList[0].SystemPrompt != "Use installed plugin guidance." || len(installedList[0].Modes) != 1 || len(installedList[0].Configs) != 1 {
		t.Fatalf("stored runtime contributions = %#v", installedList[0])
	}

	registry := pluginfs.NewRegistry(pluginfs.RegistryConfig{Store: store, Resolver: resolver})
	loaded, err := registry.Load(ctx, "superpowers")
	if err != nil {
		t.Fatalf("registry Load() error = %v", err)
	}
	if len(loaded.Skills) != 1 || loaded.Skills[0].Name != "brainstorming" {
		t.Fatalf("loaded skills = %#v, want brainstorming", loaded.Skills)
	}
	if len(loaded.MCPServers) != 1 || loaded.MCPServers[0].Name != "memory" {
		t.Fatalf("loaded mcp servers = %#v, want memory", loaded.MCPServers)
	}
	if loaded.Runtime.SystemPrompt != "Use installed plugin guidance." || len(loaded.Runtime.Agents) != 1 {
		t.Fatalf("loaded runtime contributions = %#v", loaded.Runtime)
	}
}

func TestInstallerResolvesAndInstallsPluginRoot(t *testing.T) {
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, ".codex-plugin", "plugin.json"), `{
		"name": "superpowers",
		"version": "5.1.0",
		"skills": "./skills/"
	}`)
	mustWrite(t, filepath.Join(repo, "skills", "brainstorming", "SKILL.md"), `---
name: brainstorming
description: Refine ideas before implementation.
---
# Brainstorming
`)

	storeRoot := t.TempDir()
	installer := pluginfs.NewInstaller(pluginfs.InstallerConfig{
		Resolver: pluginfs.NewResolver(),
		Store:    pluginfs.NewStore(pluginfs.StoreConfig{Root: storeRoot}),
	})
	installed, err := installer.Install(context.Background(), caelisplugin.InstallRequest{Root: repo})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if installed.Name != "superpowers" {
		t.Fatalf("installed name = %q, want superpowers", installed.Name)
	}
}
