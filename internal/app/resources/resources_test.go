package resources

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/plugin"
)

func TestDiscoverIndexesPluginsAgentsAndSkills(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	pluginDir := filepath.Join(root, "plugins", "reviewer")
	if err := os.MkdirAll(filepath.Join(home, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pluginDir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agents", "AGENTS.md"), []byte("global rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "prompts", "review.md"), []byte("review prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"id": "reviewer",
		"name": "Reviewer",
		"version": "0.1.0",
		"capabilities": ["prompt.fragment", "acp.agent"],
		"model_providers": [{"name":"reviewer-openai","uses":"openai_compatible"}],
		"stores": [{"name":"reviewer-store","uses":"sqlite"}],
		"sandbox_backends": [{"name":"reviewer-host","uses":"host"}],
		"tools": [{"name":"reviewer-shell","uses":"run_command"}],
		"prompts": [{"id":"reviewer.system","priority":50,"paths":["prompts/review.md"]}],
		"skills": [{"name":"review-skill","description":"Plugin review skill","paths":["skills/review/SKILL.md"]}],
		"acp_agents": [{"name":"reviewer","command":"reviewer-acp","args":["--stdio"],"roles":["participant"]}],
		"renderer_hints": [{"event_type":"tool_result","tool_name":"run_command","kind":"terminal"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(home, ".agents", "skills", "echo"), "echo", "home echo")
	writeSkill(t, filepath.Join(workspace, ".agents", "skills", "echo"), "echo", "workspace echo")
	writeSkill(t, filepath.Join(workspace, "skills", "local"), "local", "local skill")

	catalog, err := Discover(context.Background(), Request{
		HomeDir:      home,
		WorkspaceDir: workspace,
		PluginSources: []config.Plugin{
			{ID: "disabled", Source: filepath.Join(root, "missing"), Enabled: false},
			{Source: pluginDir, Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(catalog.Plugins) != 1 || catalog.Plugins[0].Manifest.ID != "reviewer" {
		t.Fatalf("plugins = %#v, want reviewer", catalog.Plugins)
	}
	if len(catalog.ModelProviders) != 1 || catalog.ModelProviders[0].Name != "reviewer-openai" || catalog.ModelProviders[0].Uses != "openai_compatible" {
		t.Fatalf("model providers = %#v, want reviewer-openai alias", catalog.ModelProviders)
	}
	if len(catalog.Stores) != 1 || catalog.Stores[0].Name != "reviewer-store" || catalog.Stores[0].Uses != "sqlite" {
		t.Fatalf("stores = %#v, want reviewer-store alias", catalog.Stores)
	}
	if len(catalog.Sandboxes) != 1 || catalog.Sandboxes[0].Name != "reviewer-host" || catalog.Sandboxes[0].Uses != "host" {
		t.Fatalf("sandboxes = %#v, want reviewer-host alias", catalog.Sandboxes)
	}
	if len(catalog.Tools) != 1 || catalog.Tools[0].Name != "reviewer-shell" || catalog.Tools[0].Uses != "run_command" {
		t.Fatalf("tools = %#v, want reviewer-shell alias", catalog.Tools)
	}
	if len(catalog.Prompts) != 3 {
		t.Fatalf("prompts = %#v, want plugin, global AGENTS, workspace AGENTS", catalog.Prompts)
	}
	if catalog.Prompts[0].ID != "reviewer.system" || catalog.Prompts[0].Paths[0] != filepath.Join(pluginDir, "prompts", "review.md") {
		t.Fatalf("plugin prompt = %#v, want resolved prompt path", catalog.Prompts[0])
	}
	if catalog.Prompts[1].ID != "agents.global" || catalog.Prompts[1].Text != "global rule" {
		t.Fatalf("global prompt = %#v", catalog.Prompts[1])
	}
	if catalog.Prompts[2].ID != "agents.workspace" || catalog.Prompts[2].Text != "workspace rule" {
		t.Fatalf("workspace prompt = %#v", catalog.Prompts[2])
	}
	names := skillNames(catalog.Skills)
	for _, name := range []string{"echo", "local", "review-skill", "skill-creator", "skill-installer"} {
		if !slices.Contains(names, name) {
			t.Fatalf("skills = %#v, missing %q", catalog.Skills, name)
		}
	}
	for _, skill := range catalog.Skills {
		if skill.Name == "echo" && skill.Description != "workspace echo" {
			t.Fatalf("echo skill = %#v, want workspace skill to win", skill)
		}
	}
	if len(catalog.ACPAgents) != 1 || catalog.ACPAgents[0].Command != "reviewer-acp" || catalog.ACPAgents[0].WorkDir != pluginDir {
		t.Fatalf("acp agents = %#v, want reviewer-acp", catalog.ACPAgents)
	}
	if len(catalog.RendererHints) != 1 || catalog.RendererHints[0].ToolName != "run_command" {
		t.Fatalf("renderer hints = %#v, want run_command", catalog.RendererHints)
	}
	if len(catalog.AgentFiles) != 2 {
		t.Fatalf("agent files = %#v, want global and workspace", catalog.AgentFiles)
	}
	for _, want := range []Diagnostic{
		{Kind: "plugin", ID: "disabled", Message: "plugin disabled"},
		{Kind: "plugin", ID: "reviewer", Message: "plugin loaded"},
		{Kind: "agent_file", ID: "agents.workspace", Message: "agent instruction file loaded"},
		{Kind: "skill_root", Path: filepath.Join(home, ".caelis", "skills", ".system"), Message: "system skills materialized"},
		{Kind: "skill", ID: "echo", Message: "skill overrides earlier discovery"},
		{Kind: "skill_root", Path: filepath.Join(workspace, ".agents", "skills"), Message: "skill root scanned"},
	} {
		if !hasDiagnostic(catalog.Diagnostics, want) {
			t.Fatalf("diagnostics = %#v, missing %#v", catalog.Diagnostics, want)
		}
	}
	clone := CloneCatalog(catalog)
	clone.Diagnostics[0].Meta = map[string]string{"mutated": "true"}
	if len(catalog.Diagnostics[0].Meta) != 0 {
		t.Fatalf("diagnostic meta was not cloned: %#v", catalog.Diagnostics[0].Meta)
	}
}

func TestSkillRootsIncludeSystemWorkspaceUserAndExtraDirs(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	workspace := filepath.Join(t.TempDir(), "workspace")
	extra := filepath.Join(t.TempDir(), "extra")
	got := SkillRoots(home, workspace, []string{" ", extra, extra, "~/custom"})
	want := []string{
		filepath.Join(home, ".caelis", "skills", ".system"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".caelis", "skills"),
		filepath.Join(workspace, "skills"),
		filepath.Join(workspace, ".agents", "skills"),
		extra,
		filepath.Join(home, "custom"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SkillRoots() = %#v, want %#v", got, want)
	}
}

func TestDiscoverRejectsEnabledPluginWithoutManifestID(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"name":"Missing ID"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(context.Background(), Request{
		HomeDir: root,
		PluginSources: []config.Plugin{
			{Source: pluginDir, Enabled: true},
		},
	}); err == nil {
		t.Fatal("Discover enabled plugin without id error = nil, want error")
	}
}

func TestDiscoverRejectsPluginACPAgentWithoutCommand(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"id":"bad","acp_agents":[{"name":"helper"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(context.Background(), Request{
		HomeDir: root,
		PluginSources: []config.Plugin{
			{Source: pluginDir, Enabled: true},
		},
	}); err == nil {
		t.Fatal("Discover ACP agent without command error = nil, want error")
	}
}

func TestDiscoverRejectsFactoryAliasWithoutUses(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{"id":"bad","stores":[{"name":"broken"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(context.Background(), Request{
		HomeDir: root,
		PluginSources: []config.Plugin{
			{Source: pluginDir, Enabled: true},
		},
	}); err == nil {
		t.Fatal("Discover factory alias without uses error = nil, want error")
	}
}

func writeSkill(t *testing.T, dir string, name string, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func skillNames(skills []plugin.SkillDescriptor) []string {
	out := make([]string, 0, len(skills))
	for _, skill := range skills {
		out = append(out, skill.Name)
	}
	return out
}

func hasDiagnostic(diagnostics []Diagnostic, want Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if want.Kind != "" && diagnostic.Kind != want.Kind {
			continue
		}
		if want.ID != "" && diagnostic.ID != want.ID {
			continue
		}
		if want.Path != "" && diagnostic.Path != want.Path {
			continue
		}
		if want.Message != "" && diagnostic.Message != want.Message {
			continue
		}
		return true
	}
	return false
}
