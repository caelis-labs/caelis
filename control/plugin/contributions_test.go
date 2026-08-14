package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveContributionsProjectsEnabledPlugin(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "demo")
	manifestDir := filepath.Join(root, ".caelis-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	manifest := `{
  "name":"Demo",
  "skills":[{"root":"skills"}],
  "hooks":{"SessionStart":[{"command":"echo"}]},
  "mcpServers":{"demo":{"command":"demo-mcp"}},
  "agents":[{"name":"demo-agent","command":"demo-agent"}]
}`
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := ResolveContributions([]Config{{ID: "configured-demo", Root: root, Enabled: true}})
	if err != nil {
		t.Fatalf("ResolveContributions() error = %v", err)
	}
	if len(got.SkillBundles) != 1 || !got.SkillBundles[0].Enabled || got.SkillBundles[0].Plugin != "configured-demo" {
		t.Fatalf("SkillBundles = %#v", got.SkillBundles)
	}
	if len(got.SessionStartHooks) != 1 || got.SessionStartHooks[0].PluginID != "configured-demo" {
		t.Fatalf("SessionStartHooks = %#v", got.SessionStartHooks)
	}
	if len(got.MCPServerSpecs) != 1 || got.MCPServerSpecs[0].PluginID != "configured-demo" {
		t.Fatalf("MCPServerSpecs = %#v", got.MCPServerSpecs)
	}
	if len(got.Agents) != 1 || got.Agents[0].PluginID != "configured-demo" {
		t.Fatalf("Agents = %#v", got.Agents)
	}
}

func TestResolveContributionsRejectsBrokenEnabledPlugin(t *testing.T) {
	t.Parallel()

	_, err := ResolveContributions([]Config{{ID: "broken", Root: t.TempDir(), Enabled: true}})
	if err == nil || !strings.Contains(err.Error(), `parse enabled plugin "broken"`) {
		t.Fatalf("ResolveContributions() error = %v", err)
	}
}

func TestResolveContributionsIgnoresBrokenDisabledPlugin(t *testing.T) {
	t.Parallel()

	got, err := ResolveContributions([]Config{{ID: "broken", Root: t.TempDir(), Enabled: false}})
	if err != nil {
		t.Fatalf("ResolveContributions() error = %v", err)
	}
	if len(got.SkillBundles) != 0 || len(got.SessionStartHooks) != 0 || len(got.MCPServerSpecs) != 0 || len(got.Agents) != 0 {
		t.Fatalf("ResolveContributions() = %#v, want empty", got)
	}
}
