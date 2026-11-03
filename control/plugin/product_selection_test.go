package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func desktopWorldOverrideTestPackage(t *testing.T, version string) Config {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"`+version+`"}`)
	writeAgentPluginFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"desktop-world":{"type":"stdio","command":"./server"}}}`)
	writeAgentPluginFile(t, root, "server", "fixture; never launched")
	writeAgentPluginFile(t, root, "skills/desktop-world/SKILL.md", "---\nname: desktop-world\ndescription: Selected package instructions.\n---\n"+version)
	return Config{ID: "local-alias", Name: "desktop-world", Kind: ManifestKindAgentPlugin, Root: root, Enabled: true}
}

func TestDesktopWorldWholePackagePrecedence(t *testing.T) {
	paths := RuntimePaths{StoreDir: t.TempDir()}
	global := desktopWorldOverrideTestPackage(t, "0.3.2")
	globalOut, err := WithProductContributions(Contributions{}, paths, global)
	if err != nil {
		t.Fatal(err)
	}
	assertPair := func(got Contributions, selected Config) {
		t.Helper()
		if len(got.Sources) != 1 || got.Sources[0].ID != "desktop-world" || got.Sources[0].Root != selected.Root || len(got.MCPServerSpecs) != 1 || got.MCPServerSpecs[0].PackageRoot != selected.Root || len(got.SkillBundles) != 1 || got.SkillBundles[0].PluginRoot != selected.Root {
			t.Fatalf("package was split or duplicated: %+v", got)
		}
	}
	assertPair(globalOut, global)
	explicit := desktopWorldOverrideTestPackage(t, "0.3.3")
	explicit.ID = "desktop-world"
	selected, err := ResolveContributions([]Config{explicit}, paths)
	if err != nil {
		t.Fatal(err)
	}
	global.Root = filepath.Join(t.TempDir(), "missing-lower-priority-source")
	got, err := WithProductContributions(selected, paths, global)
	if err != nil {
		t.Fatal(err)
	}
	assertPair(got, explicit)
	// Disabling/removing the override restores the embedded default without
	// deleting or changing the user's registration or package files.
	global.Enabled = false
	for _, configs := range [][]Config{{global}, nil} {
		got, err = WithProductContributions(Contributions{}, paths, configs...)
		if err != nil || len(got.Sources) != 1 || (got.Sources[0].Status != "builtin" && !strings.HasPrefix(got.Sources[0].Status, "unsupported on ")) {
			t.Fatalf("fallback = %+v, %v", got, err)
		}
	}
}

func TestDesktopWorldInvalidOverrideDoesNotFallBack(t *testing.T) {
	for _, broken := range []string{"missing", "skill", "mcp", "duplicate", "identity"} {
		t.Run(broken, func(t *testing.T) {
			configured := desktopWorldOverrideTestPackage(t, "0.3.3")
			configs := []Config{configured}
			switch broken {
			case "missing":
				configs[0].Root = filepath.Join(t.TempDir(), "missing")
			case "skill":
				if err := os.RemoveAll(filepath.Join(configured.Root, "skills")); err != nil {
					t.Fatal(err)
				}
			case "mcp":
				if err := os.Remove(filepath.Join(configured.Root, "mcp.json")); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				configs = append(configs, desktopWorldOverrideTestPackage(t, "0.3.2"))
			case "identity":
				writeAgentPluginFile(t, configured.Root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"another-plugin","version":"0.3.3"}`)
			}
			got, err := WithProductContributions(Contributions{}, RuntimePaths{StoreDir: t.TempDir()}, configs...)
			if err == nil || len(got.MCPServerSpecs) != 0 {
				t.Fatalf("invalid selection fell back: %+v, %v", got, err)
			}
		})
	}
}
