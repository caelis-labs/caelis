package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
)

func TestAgentPluginLocalUpgradeKeepsManifestIdentityAndData(t *testing.T) {
	host := &memoryHost{dir: t.TempDir()}
	service := NewService(host)
	var firstData string
	for version := 1; version <= 2; version++ {
		root := filepath.Join(t.TempDir(), fmt.Sprintf("desktop-world-v%d", version))
		writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world"}`)
		writeAgentPluginFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"desktop-world":{"type":"stdio","command":"./bin/server","cwd":"${PLUGIN_DATA}"}}}`)
		writeAgentPluginFile(t, root, "bin/server", "launcher")
		installed, err := service.Install(context.Background(), root)
		if err != nil || installed.ID != "desktop-world" || len(host.state.Plugins) != 1 {
			t.Fatalf("install v%d = %+v, %+v, %v", version, installed, host.state.Plugins, err)
		}
		contributions, err := ResolveContributions(host.state.Plugins, RuntimePaths{StoreDir: host.dir})
		if err != nil || len(contributions.MCPServerSpecs) != 1 {
			t.Fatalf("v%d contributions = %+v, %v", version, contributions, err)
		}
		data := contributions.MCPServerSpecs[0].DataDir
		if version == 1 {
			firstData = data
		} else if data != firstData {
			t.Fatalf("upgrade changed PLUGIN_DATA: %q -> %q", firstData, data)
		}
	}
}

func TestAgentPluginDTWShapeUsesOneMCPAndPersistentPrivateData(t *testing.T) {
	root := filepath.Join(t.TempDir(), "桌面 plugin with spaces")
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"0.1.0"}`)
	writeAgentPluginFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"desktop-world":{"type":"stdio","command":"./runtime/node","args":["${PLUGIN_ROOT}/mcp/server.mjs","--data-dir","${PLUGIN_DATA}"],"cwd":"${PLUGIN_DATA}"}}}`)
	writeAgentPluginFile(t, root, "runtime/node", "launcher")
	writeAgentPluginFile(t, root, "mcp/server.mjs", "server")
	writeAgentPluginFile(t, root, "skills/desktop-world/SKILL.md", "---\nname: desktop-world\ndescription: Desktop Skill\n---\n")
	// A compatibility manifest in the same package cannot launch a duplicate.
	writeAgentPluginFile(t, root, ".claude-plugin/plugin.json", `{"name":"legacy","mcpServers":{"desktop-world":{"command":"other"}}}`)

	parsed, err := ParsePlugin(root)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Kind != ManifestKindAgentPlugin || parsed.Name != "desktop-world" || len(parsed.Skills) != 1 || len(parsed.MCPServers) != 1 {
		t.Fatalf("standard package lost or duplicated contributions: %+v", parsed)
	}
	store := filepath.Join(t.TempDir(), "host-store")
	got, err := ResolveContributions([]Config{{ID: "desktop-world", Root: root, Enabled: true}}, RuntimePaths{StoreDir: store})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SkillBundles) != 1 || len(got.MCPServerSpecs) != 1 {
		t.Fatalf("contributions = %+v", got)
	}
	spec := got.MCPServerSpecs[0]
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if spec.PluginID != "desktop-world" || spec.Name != "desktop-world" || spec.Command != filepath.Join(realRoot, "runtime", "node") ||
		spec.Args[0] != filepath.Join(realRoot, "mcp", "server.mjs") || spec.Args[2] != spec.DataDir || spec.WorkDir != spec.DataDir ||
		spec.Env["PLUGIN_ROOT"] != realRoot || spec.Env["PLUGIN_DATA"] != spec.DataDir || !spec.CleanEnvironment {
		t.Fatalf("DTW standard MCP spec = %+v", spec)
	}
	if !strings.HasPrefix(spec.DataDir, filepath.Join(store, "plugins", "data")+string(filepath.Separator)) {
		t.Fatalf("PLUGIN_DATA escaped host store: %q", spec.DataDir)
	}
	if _, err := os.Stat(spec.DataDir); !os.IsNotExist(err) {
		t.Fatalf("read-only discovery created PLUGIN_DATA: %v", err)
	}
}

func TestAgentPluginInvalidMCPServerDoesNotHideSkillOrGoodServer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin")
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"example"}`)
	writeAgentPluginFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"bad":{"type":"stdio","command":"../escape"},"good":{"type":"stdio","command":"./bin/good","cwd":"${PLUGIN_DATA}"}}}`)
	writeAgentPluginFile(t, root, "bin/good", "launcher")
	writeAgentPluginFile(t, root, "skills/example/SKILL.md", "---\nname: example\ndescription: Example\n---\n")
	got, err := ParsePlugin(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 1 || len(got.MCPServers) != 1 || got.MCPServers[0].Name != "good" || len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], `"bad" skipped`) {
		t.Fatalf("failure boundary = %+v", got)
	}
	writeAgentPluginFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":[]}`)
	got, err = ParsePlugin(root)
	if err != nil || len(got.Skills) != 1 || len(got.MCPServers) != 0 || len(got.Warnings) == 0 {
		t.Fatalf("malformed component disabled more than MCP: %+v, %v", got, err)
	}
}

func TestAgentPluginSkipsOneInvalidSkillWithoutHidingOtherSkills(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin")
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"example"}`)
	writeAgentPluginFile(t, root, "skills/good/SKILL.md", "---\nname: good\ndescription: Good skill\n---\n")
	writeAgentPluginFile(t, root, "skills/bad/SKILL.md", "invalid frontmatter")
	outside := filepath.Join(t.TempDir(), "outside")
	writeAgentPluginFile(t, outside, "SKILL.md", "---\nname: outside\ndescription: Escaped skill\n---\n")
	if err := os.Symlink(outside, filepath.Join(root, "skills", "escaped")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "linked-file"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(root, "skills", "linked-file", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	contributions, err := ResolveContributions([]Config{{ID: "example", Root: root, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	metas, err := skillfs.DiscoverPluginBundleMeta(contributions.SkillBundles)
	if err != nil || len(metas) != 1 || metas[0].Name != "example:good" {
		t.Fatalf("standard Skills = %+v, %v", metas, err)
	}
}

func TestAgentPluginRejectsEscapingPackagePathAndUnknownManifestSchema(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin")
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"example"}`)
	outside := filepath.Join(t.TempDir(), "outside")
	writeAgentPluginFile(t, outside, "server", "outside")
	if err := os.Symlink(outside, filepath.Join(root, "bin")); err == nil {
		writeAgentPluginFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"escape":{"type":"stdio","command":"./bin/server"}}}`)
		got, err := ParsePlugin(root)
		if err != nil || len(got.MCPServers) != 0 || len(got.Warnings) == 0 {
			t.Fatalf("symlink escape accepted: %+v, %v", got, err)
		}
	}
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"other","name":"example"}`)
	if _, err := ParsePlugin(root); err == nil || !strings.Contains(err.Error(), "unsupported manifest schema") {
		t.Fatalf("unknown standard schema accepted: %v", err)
	}
}

func TestClaudePluginMCPMapOverrideAndProjectVariable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "claude")
	writeAgentPluginFile(t, root, ".claude-plugin/plugin.json", `{"name":"claude","mcpServers":{"one":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/one","args":["${CLAUDE_PROJECT_DIR}/file"],"env":{"PROJECT":"${CLAUDE_PROJECT_DIR}"}}}}`)
	writeAgentPluginFile(t, root, ".mcp.json", `{"mcpServers":{"one":{"command":"old"},"two":{"command":"node"}}}`)
	writeAgentPluginFile(t, root, "bin/one", "launcher")
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveContributions([]Config{{ID: "claude", Root: root, Enabled: true}}, RuntimePaths{WorkspaceDir: workspace})
	if err != nil {
		t.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCPServerSpecs) != 2 || got.MCPServerSpecs[0].Name != "one" ||
		got.MCPServerSpecs[0].Command != filepath.Join(realRoot, "bin", "one") ||
		got.MCPServerSpecs[0].Args[0] != filepath.Join(realWorkspace, "file") ||
		got.MCPServerSpecs[0].Env["PROJECT"] != realWorkspace {
		t.Fatalf("Claude override/expansion = %+v", got.MCPServerSpecs)
	}
}

func TestAgentPluginHostExecutablePathIsExplicit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lite")
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"lite"}`)
	writeAgentPluginFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"desktop-world":{"type":"stdio","command":"./bin/dtw","args":["plugin-node","--data-dir","${PLUGIN_DATA}"],"cwd":"${PLUGIN_DATA}"}}}`)
	writeAgentPluginFile(t, root, "bin/dtw", "launcher")
	nodePath := filepath.Join(t.TempDir(), "node")
	writeAgentPluginFile(t, filepath.Dir(nodePath), filepath.Base(nodePath), "binary")
	got, err := ResolveContributions([]Config{{ID: "lite", Root: root, Enabled: true, ExecutableEnv: map[string]string{"DTW_NODE_PATH": nodePath}}}, RuntimePaths{StoreDir: t.TempDir()})
	realNodePath, resolveErr := filepath.EvalSymlinks(nodePath)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if err != nil || len(got.MCPServerSpecs) != 1 || got.MCPServerSpecs[0].Env["DTW_NODE_PATH"] != realNodePath {
		t.Fatalf("explicit Lite executable path = %+v, %v", got.MCPServerSpecs, err)
	}
	_, err = ResolveContributions([]Config{{ID: "lite", Root: root, Enabled: true, ExecutableEnv: map[string]string{"DTW_NODE_PATH": "node"}}}, RuntimePaths{StoreDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("relative host executable accepted: %v", err)
	}
}

func writeAgentPluginFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
