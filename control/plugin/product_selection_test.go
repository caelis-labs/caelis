package plugin

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/skill/fs"
)

func desktopWorldOverrideTestPackage(t *testing.T, version string, identity ...string) Config {
	t.Helper()
	name := "desktop-world"
	if len(identity) > 0 {
		name = identity[0]
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"`+version+`"}`)
	writeAgentPluginFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"`+name+`":{"type":"stdio","command":"./server"}}}`)
	writeAgentPluginFile(t, root, "server", "fixture; never launched")
	writeAgentPluginFile(t, root, "skills/"+name+"/SKILL.md", "---\nname: "+name+"\ndescription: Selected package instructions.\n---\n"+version)
	return Config{ID: "local-alias", Name: "desktop-world", Kind: ManifestKindAgentPlugin, Root: root, Enabled: true}
}

func TestDesktopWorldWholePackagePrecedence(t *testing.T) {
	for _, identity := range []string{"dtw", "desktop-world"} {
		t.Run(identity, func(t *testing.T) {
			testDesktopWorldWholePackagePrecedence(t, identity)
		})
	}
}

func testDesktopWorldWholePackagePrecedence(t *testing.T, identity string) {
	paths := RuntimePaths{StoreDir: t.TempDir()}
	global := desktopWorldOverrideTestPackage(t, "999.0.0", identity)
	globalOut, err := WithProductContributions(Contributions{}, paths, global)
	if err != nil {
		t.Fatal(err)
	}
	assertPair := func(got Contributions, selected Config) {
		t.Helper()
		if len(got.Sources) != 1 || got.Sources[0].ID != "desktop-world" || got.Sources[0].Root != selected.Root || len(got.MCPServerSpecs) != 1 || got.MCPServerSpecs[0].PluginID != "desktop-world" || got.MCPServerSpecs[0].Name != identity || got.MCPServerSpecs[0].PackageRoot != selected.Root || len(got.SkillBundles) != 1 || got.SkillBundles[0].PluginRoot != selected.Root {
			t.Fatalf("package was split or duplicated: %+v", got)
		}
		metas, err := fs.DiscoverPluginBundleMeta(got.SkillBundles)
		if err != nil || len(metas) != 1 || metas[0].Name != "desktop-world:"+identity || metas[0].LocalName != identity || metas[0].PluginID != "desktop-world" || metas[0].Path != filepath.Join(selected.Root, "skills", identity, "SKILL.md") {
			t.Fatalf("selected Skill identity/root = %+v, %v", metas, err)
		}
	}
	assertPair(globalOut, global)
	explicit := desktopWorldOverrideTestPackage(t, "0.3.3", identity)
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

func TestDesktopWorldNewestDefaultKeepsWholePackage(t *testing.T) {
	for _, identity := range []string{"dtw", "desktop-world"} {
		t.Run(identity, func(t *testing.T) {
			testDesktopWorldNewestDefaultKeepsWholePackage(t, identity)
		})
	}
}

func testDesktopWorldNewestDefaultKeepsWholePackage(t *testing.T, identity string) {
	version := pinnedDesktopWorldTestVersion(t)
	for _, tc := range []struct {
		name, version string
		builtin       bool
	}{
		{"builtin-newer", "0.1.1", true},
		{"external-newer", "999.0.0", false},
		{"same-version", version, false},
		{"same-precedence-with-metadata", version + "+local", false},
		{"prerelease", version + "-rc.1", true},
		{"opaque-version", "development", false},
		{"incomplete-version", "0.1", false},
		{"absent-version", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := RuntimePaths{StoreDir: t.TempDir()}
			external := desktopWorldOverrideTestPackage(t, tc.version, identity)
			// Saved metadata and extraction names are not version authorities.
			external.Version = "9999.0.0"
			other := desktopWorldOverrideTestPackage(t, "99999.0.0")
			writeAgentPluginFile(t, other.Root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"other-plugin","version":"99999.0.0"}`)
			other.ID, other.Name = "other-plugin", "other-plugin"
			optional, err := ResolveContributions(OptionalUserConfigs([]Config{external, other}), paths)
			if err != nil {
				t.Fatal(err)
			}
			got, err := WithProductContributions(optional, paths, external, other)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Sources) != 2 || got.Sources[1].Root != other.Root || got.Sources[1].ID != "other-plugin" {
				t.Fatalf("unrelated plugin changed: %+v", got.Sources)
			}
			selected := got.Sources[0]
			wantBuiltin := tc.builtin && builtinDesktopWorldTarget != ""
			if wantBuiltin {
				if selected.Status != "builtin" || selected.Version != version || selected.Root == external.Root {
					t.Fatalf("newer embedded package was not selected: %+v", selected)
				}
			} else if selected.Status != "override" || selected.Root != external.Root || selected.Version != tc.version {
				t.Fatalf("external priority changed: %+v", selected)
			}
			if len(got.MCPServerSpecs) != 2 || len(got.SkillBundles) != 2 {
				t.Fatalf("split or duplicated package: %+v", got)
			}
			spec, bundle := got.MCPServerSpecs[0], got.SkillBundles[0]
			if spec.PluginID != selected.ID || spec.PackageRoot != selected.Root || spec.PackageVersion != selected.Version || !PathWithinRoot(selected.Root, spec.Command) || bundle.PluginRoot != selected.Root || spec.SourceTrusted {
				t.Fatalf("launch/Skill/source mismatch: %+v, %+v, %+v", selected, spec, bundle)
			}
			metas, err := fs.DiscoverPluginBundleMeta(got.SkillBundles)
			if err != nil || len(metas) != 2 || !PathWithinRoot(selected.Root, metas[0].Path) || metas[0].PluginID != "desktop-world" || metas[0].Name != "desktop-world:"+spec.Name || metas[0].LocalName != spec.Name {
				t.Fatalf("selected Skill catalog = %+v, %v", metas, err)
			}
			if !wantBuiltin && spec.Name != identity {
				t.Fatalf("external service identity = %q, want %q", spec.Name, identity)
			}
			if got.MCPServerSpecs[1].PackageRoot != other.Root || got.SkillBundles[1].PluginRoot != other.Root {
				t.Fatal("version ordering crossed plugin identities")
			}
		})
	}
}

func TestProductVersionOrdering(t *testing.T) {
	for _, tc := range []struct {
		candidate, current string
		newer              bool
	}{
		{"0.3.5", "0.3.4", true},
		{"0.3.5", "v0.3.5-rc.1", true},
		{"0.3.5+official", "0.3.5+local", false},
		{"0.3.5", "0.3.6", false},
		{"0.3.5", "0.3", false},
		{"0.3.5", "", false},
		{"development", "0.3.4", false},
		{"0.3.5", "01.2.3", false},
	} {
		if got := newerProductVersion(tc.candidate, tc.current); got != tc.newer {
			t.Errorf("newerProductVersion(%q, %q) = %t", tc.candidate, tc.current, got)
		}
	}
}

func TestDesktopWorldPairIdentityValidation(t *testing.T) {
	for _, identity := range []string{"dtw", "desktop-world"} {
		otherIdentity := "dtw"
		if identity == "dtw" {
			otherIdentity = "desktop-world"
		}
		for _, broken := range []string{"mixed", "unexpected-service", "both-services", "both-skills"} {
			t.Run(identity+"/"+broken, func(t *testing.T) {
				configured := desktopWorldOverrideTestPackage(t, "development", identity)
				wantServices := []string{identity}
				switch broken {
				case "mixed":
					wantServices = []string{otherIdentity}
					writeAgentPluginFile(t, configured.Root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"`+otherIdentity+`":{"type":"stdio","command":"./server"}}}`)
				case "unexpected-service":
					wantServices = []string{"unrelated"}
					writeAgentPluginFile(t, configured.Root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"unrelated":{"type":"stdio","command":"./server"}}}`)
				case "both-services":
					wantServices = []string{"desktop-world", "dtw"}
					writeAgentPluginFile(t, configured.Root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"dtw":{"type":"stdio","command":"./server"},"desktop-world":{"type":"stdio","command":"./server"}}}`)
				case "both-skills":
					writeAgentPluginFile(t, configured.Root, "skills/"+otherIdentity+"/SKILL.md", "---\nname: "+otherIdentity+"\ndescription: Conflicting identity.\n---\n")
				}
				paths := RuntimePaths{StoreDir: t.TempDir()}
				explicit := configured
				explicit.ID = "desktop-world"
				selected, err := ResolveContributions([]Config{explicit}, paths)
				if err != nil {
					t.Fatal(err)
				}
				var services []string
				for _, spec := range selected.MCPServerSpecs {
					services = append(services, spec.Name)
				}
				slices.Sort(services)
				if !slices.Equal(services, wantServices) {
					t.Fatalf("fixture services = %v, want %v", services, wantServices)
				}
				// Both default and explicit selection enforce the package contract.
				if _, err := WithProductContributions(Contributions{}, paths, configured); err == nil {
					t.Fatal("invalid global pair was accepted")
				}
				if _, err := WithProductContributions(selected, paths); err == nil {
					t.Fatal("invalid explicit pair was accepted")
				}
			})
		}
	}
}

func TestDesktopWorldPairRejectsSplitOrDuplicateContributions(t *testing.T) {
	for _, identity := range []string{"dtw", "desktop-world"} {
		for _, broken := range []string{"service-root", "skill-root", "missing-source", "duplicate-source", "duplicate-service"} {
			t.Run(identity+"/"+broken, func(t *testing.T) {
				paths := RuntimePaths{StoreDir: t.TempDir()}
				configured := desktopWorldOverrideTestPackage(t, "development", identity)
				configured.ID = "desktop-world"
				selected, err := ResolveContributions([]Config{configured}, paths)
				if err != nil {
					t.Fatal(err)
				}
				switch broken {
				case "service-root":
					selected.MCPServerSpecs[0].PackageRoot = t.TempDir()
				case "skill-root":
					other := desktopWorldOverrideTestPackage(t, "development", identity)
					other.ID = "desktop-world"
					foreign, err := ResolveContributions([]Config{other}, paths)
					if err != nil {
						t.Fatal(err)
					}
					selected.SkillBundles = foreign.SkillBundles
				case "missing-source":
					selected.Sources = nil
				case "duplicate-source":
					selected.Sources = append(selected.Sources, selected.Sources[0])
				case "duplicate-service":
					selected.MCPServerSpecs = append(selected.MCPServerSpecs, selected.MCPServerSpecs[0])
				}
				if err := validateProductPair(selected); err == nil {
					t.Fatalf("invalid pair accepted: %+v", selected)
				}
			})
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
