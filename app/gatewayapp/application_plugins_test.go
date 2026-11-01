package gatewayapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func selectedPluginFixture(t *testing.T, name, version string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		".claude-plugin/plugin.json":    `{"name":"` + name + `","version":"` + version + `"}`,
		".mcp.json":                     `{"mcpServers":{"desktop-world":{"command":"synthetic-native"}}}`,
		"skills/desktop-world/SKILL.md": "---\nname: desktop-world\ndescription: Synthetic desktop instructions.\n---\nVERSION_" + version,
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestApplicationNativePluginIdentityPolicyAndPairing(t *testing.T) {
	root := selectedPluginFixture(t, "desktop-world", "0.2.0")
	profile := sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: root, MCPServers: []sessionprofile.MCPServer{{Name: "desktop-world", Transport: "stdio", Command: "verified-bot-proxy", Args: []string{"generation-1"}, WorkDir: root}}}}}
	paths := applicationPluginPaths(t.TempDir(), "../../untrusted-preferred-id", t.TempDir())
	if strings.Contains(paths.StoreDir, "untrusted-preferred-id") {
		t.Fatal("Session identity used as a path")
	}
	contribution, err := applicationPluginContributions(profile, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.MCPServerSpecs) != 1 || len(contribution.Sources) != 1 {
		t.Fatal("package was not selected once")
	}
	spec := contribution.MCPServerSpecs[0]
	if spec.PluginID != "desktop-world" || spec.PackageRoot != root || spec.PackageVersion != "0.2.0" || spec.Command != "verified-bot-proxy" || spec.WorkDir != root || !spec.CleanEnvironment || spec.SourceTrusted || len(spec.SourceTrustedTools) != 3 || spec.ToolResourceParameters["exec"] != "app" {
		t.Fatalf("lost native identity or policy: %+v", spec)
	}
	catalog, _, err := assembleApplicationSkills(profile, true, contribution.SkillBundles...)
	if err != nil || len(catalog.Metas()) != 1 || catalog.Metas()[0].Name != "desktop-world:desktop-world" || !strings.HasPrefix(catalog.Metas()[0].Path, root+string(filepath.Separator)) {
		t.Fatalf("unpaired native Skill: %+v %v", catalog.Metas(), err)
	}
	raw, _ := json.Marshal(spec)
	if strings.Contains(string(raw), "SourceTrusted") || strings.Contains(string(raw), "ToolResourceParameters") {
		t.Fatal("policy serialized as a client claim")
	}
	legacy := selectedPluginFixture(t, "desktop-world", "0.1.3")
	old, err := applicationPluginContributions(sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: legacy}}}, paths)
	if err != nil || len(old.MCPServerSpecs[0].SourceTrustedTools) != 0 || len(old.MCPServerSpecs[0].ToolResourceParameters) != 0 {
		t.Fatalf("legacy policy widened: %+v %v", old, err)
	}
	ordinary := selectedPluginFixture(t, "ordinary-plugin", "0.2.0")
	plain, err := applicationPluginContributions(sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: ordinary}}}, paths)
	if err != nil || len(plain.MCPServerSpecs[0].SourceTrustedTools) != 0 || len(plain.MCPServerSpecs[0].ToolResourceParameters) != 0 {
		t.Fatalf("server spelling gained DTW authority: %+v %v", plain, err)
	}
}

func TestApplicationNativePluginRejectsDuplicateSourcesAndProxySpoofing(t *testing.T) {
	a, b := selectedPluginFixture(t, "desktop-world", "0.2.0"), selectedPluginFixture(t, "desktop-world", "0.2.1")
	paths := plugin.RuntimePaths{StoreDir: t.TempDir()}
	for name, profile := range map[string]sessionprofile.Profile{
		"two versions":       {Plugins: []sessionprofile.Plugin{{Root: a}, {Root: b}}},
		"ordinary duplicate": {Plugins: []sessionprofile.Plugin{{Root: a}}, MCPServers: []sessionprofile.MCPServer{{Name: "legacy_proxy", WorkDir: a}}},
		"overlapping name":   {Plugins: []sessionprofile.Plugin{{Root: a}}, MCPServers: []sessionprofile.MCPServer{{Name: "desktop_world"}}},
		"undeclared proxy":   {Plugins: []sessionprofile.Plugin{{Root: a, MCPServers: []sessionprofile.MCPServer{{Name: "forged", Command: "not-started"}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := applicationPluginContributions(profile, paths); err == nil {
				t.Fatal("ambiguous/spoofed native source admitted")
			}
		})
	}
	other := selectedPluginFixture(t, "other-plugin", "0.2.0")
	second, err := applicationPluginContributions(sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: other}}}, paths)
	if err != nil {
		t.Fatal(err)
	}
	first, err := applicationPluginContributions(sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: a}}}, paths)
	if err != nil {
		t.Fatal(err)
	}
	second.SkillBundles[0].Namespace = first.SkillBundles[0].Namespace
	if _, _, err := assembleApplicationSkills(sessionprofile.Profile{}, true, append(first.SkillBundles, second.SkillBundles...)...); err == nil {
		t.Fatal("another package replaced the selected native Skill through a shared namespace")
	}
	c, err := applicationPluginContributions(sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: a}}}, paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{filepath.Join(a, "skills", "desktop-world"), filepath.Join(b, "skills", "desktop-world")} {
		if _, _, err := assembleApplicationSkills(sessionprofile.Profile{SkillRoots: []string{root}}, true, c.SkillBundles...); err == nil {
			t.Fatal("native MCP admitted mismatched/redundant ordinary Skill")
		}
	}
}

func TestApplicationNativePluginPrivateDataDoesNotCrossSessionOrHost(t *testing.T) {
	root := selectedPluginFixture(t, "desktop-world", "0.2.0")
	for name, body := range map[string]string{
		"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"0.2.0"}`,
		"mcp.json":    `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"desktop-world":{"type":"stdio","command":"./bin/server","cwd":"${PLUGIN_DATA}"}}}`,
		"bin/server":  "synthetic executable never launched",
	} {
		file := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store := t.TempDir()
	profile := sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: root}}}
	a, err := applicationPluginContributions(profile, applicationPluginPaths(store, "a", t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := applicationPluginContributions(profile, applicationPluginPaths(store, "b", t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := plugin.ResolveContributions([]plugin.Config{{ID: "desktop-world", Root: root, Enabled: true}}, plugin.RuntimePaths{StoreDir: store})
	if err != nil {
		t.Fatal(err)
	}
	first, second, global := a.MCPServerSpecs[0].DataDir, b.MCPServerSpecs[0].DataDir, standalone.MCPServerSpecs[0].DataDir
	if first == "" || first == second || first == global || second == global {
		t.Fatalf("private plugin data shared across authorities: %q %q %q", first, second, global)
	}
	profile.Plugins[0].MCPServers = []sessionprofile.MCPServer{{Name: "desktop-world", Transport: "stdio", Command: "verified-bot-proxy", WorkDir: root}}
	proxied, err := applicationPluginContributions(profile, applicationPluginPaths(store, "a", t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if spec := proxied.MCPServerSpecs[0]; spec.DataDir != "" || len(spec.Env) != 0 || spec.Command != "verified-bot-proxy" || spec.PluginID != "desktop-world" {
		t.Fatalf("proxy inherited native launch environment/data: %+v", spec)
	}
}
