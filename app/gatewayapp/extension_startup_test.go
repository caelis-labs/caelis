package gatewayapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/mcpconfig"
)

func TestLocalHostStartsWithBrokenOptionalExtensions(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	storeDir := filepath.Join(root, "store")
	regular := filepath.Join(root, "regular-skills")
	for _, dir := range []string{home, workspace, regular, filepath.Join(home, ".agents")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeExtensionTestFile(t, filepath.Join(home, ".agents", "mcp.json"), `{"mcpServers":`)
	writeExtensionTestFile(t, filepath.Join(regular, "good", "SKILL.md"), "---\nname: good\ndescription: Usable skill\n---\n# Good\n")
	writeExtensionTestFile(t, filepath.Join(regular, "bad", "SKILL.md"), "---\nname: [invalid\n---\n# Bad\n")
	goodPlugin := filepath.Join(root, "good-plugin")
	badPlugin := filepath.Join(root, "bad-plugin")
	buildMinimalPluginDir(t, goodPlugin, `{"name":"good-plugin","skills":[{"root":"skills"}]}`)
	buildMinimalPluginDir(t, badPlugin, `{"name":`)
	writeExtensionTestFile(t, filepath.Join(goodPlugin, "skills", "usable", "SKILL.md"), "---\nname: usable\ndescription: Usable plugin skill\n---\n# Usable\n")
	writeExtensionTestFile(t, filepath.Join(goodPlugin, "skills", "broken", "SKILL.md"), "---\nname: [invalid\n---\n# Broken\n")
	configStore := newAppConfigStore(storeDir)
	if err := configStore.Save(AppConfig{Plugins: []PluginConfig{
		{ID: "good-plugin", Root: goodPlugin, Enabled: true},
		{ID: "bad-plugin", Root: badPlugin, Enabled: true},
	}}); err != nil {
		t.Fatal(err)
	}
	doc, err := configStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an already-persisted damaged optional MCP entry. The explicit
	// Save path correctly rejects this candidate.
	doc.MCPServers = mcpconfig.Servers{"broken": {}, "unreachable": {Command: filepath.Join(root, "absent-mcp")}}
	if err := configStore.Save(doc); err == nil {
		t.Fatal("explicit Save accepted invalid MCP configuration")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configStore.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	stack, err := NewLocalStack(Config{
		AppName: "CAELIS", StoreDir: storeDir, WorkspaceCWD: workspace,
		SkillDirs: []string{regular}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatalf("Host startup with broken extensions: %v", err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	catalog, err := stack.WorkspaceReads().CurrentSkillCatalog(context.Background(), stack.composition.workspace)
	if err != nil {
		t.Fatalf("current Skill catalog: %v", err)
	}
	names := map[string]bool{}
	for _, meta := range catalog.Metas() {
		names[meta.Name] = true
	}
	if !names["good"] || !names["good-plugin:usable"] || names["bad"] || names["good-plugin:broken"] {
		t.Fatalf("current Skill catalog = %+v", names)
	}
	plugins, err := stack.ControlPluginReads().List(context.Background())
	if err != nil || len(plugins) != 2 {
		t.Fatalf("Plugin list = %+v, %v", plugins, err)
	}
	for _, info := range plugins {
		if info.ID == "bad-plugin" && (info.Status == "active" || strings.TrimSpace(info.Warning) == "") {
			t.Fatalf("broken Plugin status = %+v", info)
		}
		if info.ID == "good-plugin" && (len(info.Skills) != 1 || info.Skills[0] != "good-plugin:usable") {
			t.Fatalf("Plugin Skill list includes broken Skill: %+v", info.Skills)
		}
	}
	plan, err := stack.composition.loadGatewayBuildPlan(stack.composition.sandbox, stack.composition.activeRuntime)
	if err != nil {
		t.Fatalf("future Runtime plan: %v", err)
	}
	if plan.ReleasePluginCache != nil {
		defer func() { _ = plan.ReleasePluginCache() }()
	}
	if len(plan.MCPServers) != 1 || plan.MCPServers[0].Name != "unreachable" {
		t.Fatalf("isolated MCP specs = %+v", plan.MCPServers)
	}
}

func writeExtensionTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
