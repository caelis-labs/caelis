package gatewayapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestFixedProductCapabilitiesSurviveOptionalEmptySelection(t *testing.T) {
	store := t.TempDir()
	for _, plugins := range [][]sessionprofile.Plugin{nil, {}} {
		got, err := applicationRuntimePluginContributions(sessionprofile.Profile{Plugins: plugins}, applicationPluginPaths(store, "native-session", t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Sources) != 1 || got.Sources[0].ID != "desktop-world" {
			t.Fatal("optional selection controlled the fixed product capability")
		}
	}
	inherited, err := resolveGatewayPluginContributions(nil, store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(inherited.Sources) != 1 || inherited.Sources[0].ID != "desktop-world" {
		t.Fatal("ordinary Worker did not receive the product capability")
	}
}

func TestDesktopWorldOverridePrecedenceAcrossProfilesAndWorkers(t *testing.T) {
	makePackage := func(name, version string) string {
		t.Helper()
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		for path, body := range map[string]string{
			"plugin.json":                  `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"` + name + `","version":"` + version + `"}`,
			"mcp.json":                     `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"` + name + `":{"type":"stdio","command":"./server"}}}`,
			"server":                       "fixture; never launched",
			"skills/" + name + "/SKILL.md": "---\nname: " + name + "\ndescription: Selected package.\n---\n" + root,
		} {
			file := filepath.Join(root, path)
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	root := makePackage("desktop-world", "0.1.1")
	globalRoot := makePackage("desktop-world", "999.0.0")
	otherRoot := makePackage("other-plugin", "9999.0.0")
	defaults := []plugin.Config{{ID: "alias", Root: globalRoot, Enabled: true}, {ID: "other-plugin", Root: otherRoot, Enabled: true}}
	paths := applicationPluginPaths(t.TempDir(), "profile", t.TempDir())
	selected := sessionprofile.Profile{Plugins: []sessionprofile.Plugin{{Root: root, MCPServers: []sessionprofile.MCPServer{{Name: "desktop-world", Transport: "stdio", Command: "must-never-launch", WorkDir: root}}}}}
	got, err := applicationRuntimePluginContributions(selected, paths, defaults...)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 || got.Sources[0].Root != root || len(got.MCPServerSpecs) != 1 || got.MCPServerSpecs[0].PackageRoot != root || got.MCPServerSpecs[0].Command != "must-never-launch" || len(got.SkillBundles) != 1 || got.SkillBundles[0].PluginRoot != root {
		t.Fatal("explicit Session package and its bound host launch were split or duplicated")
	}
	resource := &applicationMCPResource{plugins: selected.Plugins}
	changed := selected
	changed.Plugins = []sessionprofile.Plugin{{Root: globalRoot}}
	if resource.matches(changed) {
		t.Fatal("source change reused the old Session connection")
	}
	for _, optional := range [][]sessionprofile.Plugin{nil, {}} {
		got, err = applicationRuntimePluginContributions(sessionprofile.Profile{Plugins: optional}, paths, defaults...)
		if err != nil || len(got.Sources) != 1 || got.Sources[0].Root != globalRoot || got.MCPServerSpecs[0].PackageRoot != globalRoot || got.SkillBundles[0].PluginRoot != globalRoot {
			t.Fatalf("Bot/Worker default or optional isolation = %+v, %v", got, err)
		}
	}
	ordinary, err := resolveGatewayPluginContributions(defaults, paths.ProductStoreDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(ordinary.Sources) != 2 || ordinary.Sources[0].Root != globalRoot {
		t.Fatal("inherited optional plugins or canonical DTW selection lost")
	}
	olderRoot := makePackage("desktop-world", "0.1.1")
	defaults[0].Root = olderRoot
	builtin, err := plugin.ProductContributions(paths)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := olderRoot
	if builtin.Sources[0].Status == "builtin" {
		wantRoot = builtin.Sources[0].Root
	}
	for _, optional := range [][]sessionprofile.Plugin{nil, {}} {
		got, err = applicationRuntimePluginContributions(sessionprofile.Profile{Plugins: optional}, paths, defaults...)
		if err != nil || len(got.Sources) != 1 || got.Sources[0].Root != wantRoot || got.MCPServerSpecs[0].PackageRoot != wantRoot || got.SkillBundles[0].PluginRoot != wantRoot {
			t.Fatalf("newest default in Bot/Worker profile = %+v, %v", got, err)
		}
	}
	ordinary, err = resolveGatewayPluginContributions(defaults, paths.ProductStoreDir, t.TempDir())
	if err != nil || len(ordinary.Sources) != 2 || ordinary.Sources[0].Root != wantRoot || ordinary.Sources[1].Root != otherRoot {
		t.Fatalf("newest ordinary default or optional isolation = %+v, %v", ordinary, err)
	}
	defaults[0].Enabled = false
	got, err = applicationRuntimePluginContributions(sessionprofile.Profile{}, paths, defaults...)
	if err != nil || len(got.Sources) != 1 || (got.Sources[0].Status != "builtin" && !strings.HasPrefix(got.Sources[0].Status, "unsupported on ")) {
		t.Fatalf("disable did not restore builtin: %+v, %v", got, err)
	}
	selected.Plugins[0].Root = filepath.Join(root, "missing")
	if _, err = applicationRuntimePluginContributions(selected, paths, defaults...); err == nil {
		t.Fatal("broken explicit selection silently fell back")
	}
}

func TestFixedProductNamespaceCannotBeShadowedByOrdinaryMCP(t *testing.T) {
	for _, name := range []string{"dtw", "dtw__extra", "dtw--extra", "desktop-world", "desktop_world", "desktop-world__extra"} {
		for _, id := range []string{"", "user", "dtw", "desktop_world"} {
			_, err := mergeRuntimeMCPSpecs([]mcp.ServerSpec{{PluginID: id, Name: name}}, nil)
			if err == nil {
				t.Fatalf("MCP source %q with plugin ID %q shadowed the fixed tool namespace", name, id)
			}
		}
	}
	for _, name := range []string{"dtw", "desktop-world"} {
		got, err := mergeRuntimeMCPSpecs(nil, []plugin.MCPServerSpec{{PluginID: "desktop-world", Name: name}})
		if err != nil || len(got) != 1 || got[0].Name != name {
			t.Fatalf("selected DTW package lost its own namespace: %+v, %v", got, err)
		}
	}
	for _, name := range []string{"dtw-extra", "dtw_extra", "dtworld", "desktop-world-extra", "other"} {
		got, err := mergeRuntimeMCPSpecs([]mcp.ServerSpec{{Name: name}}, nil)
		if err != nil || len(got) != 1 {
			t.Fatalf("unrelated MCP namespace %q was reserved: %+v, %v", name, got, err)
		}
	}
}
