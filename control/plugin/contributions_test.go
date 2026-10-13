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

func TestResolveAvailableContributionsSkipsBrokenPluginAndRecovers(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good")
	broken := filepath.Join(root, "broken")
	for _, dir := range []string{good, broken} {
		if err := os.MkdirAll(filepath.Join(dir, ".caelis-plugin"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"name":"demo","skills":[{"root":"skills"}]}`
	if err := os.WriteFile(filepath.Join(good, ".caelis-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	brokenPath := filepath.Join(broken, ".caelis-plugin", "plugin.json")
	if err := os.WriteFile(brokenPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	configs := []Config{{ID: "good", Root: good, Enabled: true}, {ID: "broken", Root: broken, Enabled: true}}
	if _, err := ResolveContributions(configs); err == nil {
		t.Fatal("strict configuration accepted broken manifest")
	}
	got, err := ResolveAvailableContributions(configs)
	if err != nil || len(got.SkillBundles) != 1 || got.SkillBundles[0].Plugin != "good" {
		t.Fatalf("available contributions = %+v, %v", got, err)
	}
	if err := os.WriteFile(brokenPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveAvailableContributions(configs)
	if err != nil || len(got.SkillBundles) != 2 {
		t.Fatalf("repaired contributions = %+v, %v", got, err)
	}
}

func TestResolveAvailableContributionsKeepsValidNativeMCPAndSkill(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mixed")
	manifestDir := filepath.Join(root, ".caelis-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(manifestDir, "plugin.json")
	write := func(bad string) {
		t.Helper()
		manifest := `{"name":"mixed","skills":[{"root":"skills"}],"mcpServers":{"good":{"command":"good-mcp"},"bad":` + bad + `}}`
		if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"args":"wrong-type"}`)
	config := []Config{{ID: "mixed", Root: root, Enabled: true}}
	if _, err := ResolveContributions(config); err == nil || !strings.Contains(err.Error(), `server "bad"`) {
		t.Fatalf("strict contribution validation = %v", err)
	}
	got, err := ResolveAvailableContributions(config)
	if err != nil || len(got.SkillBundles) != 1 || len(got.MCPServerSpecs) != 1 || got.MCPServerSpecs[0].Name != "good" {
		t.Fatalf("available contributions = %+v, %v", got, err)
	}
	parsed, err := ParseConfiguredAvailable(config[0])
	if err != nil || len(parsed.Warnings) != 1 || !strings.Contains(parsed.Warnings[0], `"bad"`) {
		t.Fatalf("degraded Plugin warning = %+v, %v", parsed.Warnings, err)
	}
	write(`{"command":"repaired-mcp"}`)
	got, err = ResolveAvailableContributions(config)
	if err != nil || len(got.MCPServerSpecs) != 2 {
		t.Fatalf("repaired contributions = %+v, %v", got, err)
	}
}
