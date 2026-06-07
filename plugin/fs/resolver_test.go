package fs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
	pluginfs "github.com/OnslaughtSnail/caelis/plugin/fs"
)

func TestResolverImportsCodexManifestAndDiscoversSkills(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".codex-plugin", "plugin.json"), `{
		"name": "superpowers",
		"version": "5.1.0",
		"description": "Planning workflows",
		"skills": "./skills/",
		"mcpServers": [{
			"name": "memory",
			"transport": "stdio",
			"command": "node",
			"args": ["server.js"]
		}]
	}`)
	mustWrite(t, filepath.Join(root, "skills", "brainstorming", "SKILL.md"), `---
name: brainstorming
description: Refine ideas before implementation.
---
# Brainstorming
`)
	mustWrite(t, filepath.Join(root, "skills", "writing-plans", "SKILL.md"), `---
name: writing-plans
description: Create implementation plans.
---
# Writing Plans
`)

	resolver := pluginfs.NewResolver()
	resolved, err := resolver.Resolve(context.Background(), caelisplugin.ResolveRequest{Root: root})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Manifest.Name != "superpowers" {
		t.Fatalf("manifest name = %q, want superpowers", resolved.Manifest.Name)
	}
	if len(resolved.MCPServers) != 1 || resolved.MCPServers[0].Name != "memory" || resolved.MCPServers[0].Command != "node" {
		t.Fatalf("mcp servers = %#v, want memory stdio server", resolved.MCPServers)
	}
	if got, want := len(resolved.Skills), 2; got != want {
		t.Fatalf("discovered skills = %d, want %d", got, want)
	}
	names := map[string]bool{}
	for _, one := range resolved.Skills {
		names[one.Name] = true
		if one.Metadata["plugin"] != "superpowers" {
			t.Fatalf("skill %s metadata plugin = %#v, want superpowers", one.Name, one.Metadata["plugin"])
		}
	}
	if !names["brainstorming"] || !names["writing-plans"] {
		t.Fatalf("skill names = %#v, want brainstorming and writing-plans", names)
	}
}

func TestResolverNormalizesRuntimeContributions(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".caelis-plugin", "plugin.json"), `{
		"name": "ops-pack",
		"version": "1.2.3",
		"contributions": {
			"skills": [{"root": "./skills", "namespace": "ops"}],
			"agents": [{
				"name": "reviewer",
				"description": "Review implementation changes.",
				"command": "caelis",
				"args": ["acp"]
			}],
			"modes": [{"id": "safe", "name": "Safe"}],
			"configs": [{"id": "verbosity", "name": "Verbosity", "defaultValue": "low"}],
			"systemPrompt": "Prefer reversible operations.",
			"policyMode": "workspace-write",
			"extraReadRoots": ["./docs"],
			"extraWriteRoots": ["./generated"]
		}
	}`)
	mustWrite(t, filepath.Join(root, "skills", "deploy-check", "SKILL.md"), `---
name: deploy-check
description: Check deployment readiness.
---
# Deploy Check
`)

	resolver := pluginfs.NewResolver()
	resolved, err := resolver.Resolve(context.Background(), caelisplugin.ResolveRequest{Root: root})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Runtime.SystemPrompt != "Prefer reversible operations." || resolved.Runtime.PolicyMode != "workspace-write" {
		t.Fatalf("runtime prompt/policy = %#v", resolved.Runtime)
	}
	if len(resolved.Runtime.Skills) != 1 || resolved.Runtime.Skills[0].Name != "deploy-check" {
		t.Fatalf("runtime skills = %#v", resolved.Runtime.Skills)
	}
	if len(resolved.Runtime.Agents) != 1 || resolved.Runtime.Agents[0].Name != "reviewer" {
		t.Fatalf("runtime agents = %#v", resolved.Runtime.Agents)
	}
	if len(resolved.Runtime.Modes) != 1 || resolved.Runtime.Modes[0].ID != "safe" {
		t.Fatalf("runtime modes = %#v", resolved.Runtime.Modes)
	}
	if len(resolved.Runtime.Configs) != 1 || resolved.Runtime.Configs[0].ID != "verbosity" {
		t.Fatalf("runtime configs = %#v", resolved.Runtime.Configs)
	}
	if len(resolved.Runtime.ExtraReadRoots) != 1 || resolved.Runtime.ExtraReadRoots[0] != "./docs" {
		t.Fatalf("runtime read roots = %#v", resolved.Runtime.ExtraReadRoots)
	}
}

func mustWrite(t *testing.T, path string, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
