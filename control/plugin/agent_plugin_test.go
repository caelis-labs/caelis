package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
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
	host := &memoryHost{dir: t.TempDir()}
	if _, err := NewService(host).Install(t.Context(), root); err != nil {
		t.Fatalf("install package with mixed Skills: %v", err)
	}
	contributions, err := ResolveContributions(host.state.Plugins)
	if err != nil {
		t.Fatal(err)
	}
	metas, err := skillfs.DiscoverPluginBundleMeta(contributions.SkillBundles)
	if err != nil || len(metas) != 1 || metas[0].Name != "example:good" {
		t.Fatalf("standard Skills = %+v, %v", metas, err)
	}
}

func TestAgentPluginSkillDescriptionUnicodeCharacterBoundary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		characters int
		want       int
	}{
		{"chinese-400", 400, 1},
		{"chinese-1024", 1024, 1},
		{"chinese-1025", 1025, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "plugin")
			writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"example"}`)
			writeAgentPluginFile(t, root, "skills/example/SKILL.md", "---\nname: example\ndescription: "+strings.Repeat("中", tc.characters)+"\n---\n")
			contributions, err := ResolveContributions([]Config{{ID: "example", Root: root, Enabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			metas, err := skillfs.DiscoverPluginBundleMeta(contributions.SkillBundles)
			if err != nil || len(metas) != tc.want {
				t.Fatalf("%d Chinese characters: Skills = %+v, %v; want %d", tc.characters, metas, err, tc.want)
			}
		})
	}
}

func TestAgentPluginSkillYAMLFrontmatterThroughContributions(t *testing.T) {
	for _, tc := range []struct {
		name, front, description string
	}{
		{"comment", "name: example # stable identifier\ndescription: Commented name", "Commented name"},
		{"metadata", "name: example\ndescription: Example skill\nmetadata:\n  name: Display Name", "Example skill"},
		{"folded", "name: example\ndescription: >-\n  Use this skill to inspect\n  plugin state.", "Use this skill to inspect plugin state."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "plugin")
			writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"example"}`)
			writeAgentPluginFile(t, root, "skills/example/SKILL.md", "---\n"+tc.front+"\n---\n# Body\n")
			contributions, err := ResolveContributions([]Config{{ID: "example", Root: root, Enabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			metas, err := skillfs.DiscoverPluginBundleMeta(contributions.SkillBundles)
			if err != nil || len(metas) != 1 || metas[0].Name != "example:example" || metas[0].Description != tc.description {
				t.Fatalf("YAML metadata = %+v, %v; want %q", metas, err, tc.description)
			}
		})
	}
}

func TestAgentPluginSkillSymlinkBoundaryIsPackageRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "plugin")
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"example"}`)
	writeAgentPluginFile(t, root, "shared/deploy/SKILL.md", "---\nname: deploy\ndescription: Bundled skill\n---\n")
	writeAgentPluginFile(t, root, "shared/linked/SKILL.md", "---\nname: linked\ndescription: Linked directory\n---\n")
	writeAgentPluginFile(t, parent, "outside/SKILL.md", "---\nname: outside\ndescription: Escaped skill\n---\n")
	for _, name := range []string{"deploy", "outside"} {
		if err := os.MkdirAll(filepath.Join(root, "skills", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "shared", "deploy", "SKILL.md"), filepath.Join(root, "skills", "deploy", "SKILL.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(parent, "outside", "SKILL.md"), filepath.Join(root, "skills", "outside", "SKILL.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "shared", "linked"), filepath.Join(root, "skills", "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	host := &memoryHost{dir: t.TempDir()}
	if _, err := NewService(host).Install(t.Context(), root); err != nil {
		t.Fatalf("install package with internal Skill links: %v", err)
	}
	contributions, err := ResolveContributions(host.state.Plugins)
	if err != nil {
		t.Fatal(err)
	}
	metas, err := skillfs.DiscoverPluginBundleMeta(contributions.SkillBundles)
	if err != nil || len(metas) != 2 || metas[0].Name != "example:deploy" || metas[1].Name != "example:linked" {
		t.Fatalf("package symlink boundary = %+v, %v", metas, err)
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

func TestClaudePluginBadServerIsolatedAcrossRootAndInlineMaps(t *testing.T) {
	root := filepath.Join(t.TempDir(), "claude")
	writeAgentPluginFile(t, root, ".claude-plugin/plugin.json", `{"name":"claude","mcpServers":{"bad-inline":{"command":42},"root-good":{"command":""},"inline-good":{"command":"python"}}}`)
	writeAgentPluginFile(t, root, ".mcp.json", `{"mcpServers":{"bad-root":{"command":42},"root-good":{"command":"node"}}}`)
	contributions, err := ResolveContributions([]Config{{ID: "claude", Root: root, Enabled: true}})
	if err != nil || len(contributions.MCPServerSpecs) != 2 ||
		contributions.MCPServerSpecs[0].Name != "inline-good" || contributions.MCPServerSpecs[0].Command != "python" ||
		contributions.MCPServerSpecs[1].Name != "root-good" || contributions.MCPServerSpecs[1].Command != "node" {
		t.Fatalf("server isolation/precedence = %+v, %v", contributions.MCPServerSpecs, err)
	}
	parsed, err := ParsePlugin(root)
	if err != nil || len(parsed.Warnings) < 2 {
		t.Fatalf("invalid entries lacked diagnostics: %+v, %v", parsed.Warnings, err)
	}
}

func TestClaudePluginHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_CLAUDE_ENV_HELPER") != "1" {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "claude-env-test", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "environment", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: os.Getenv("CLAUDE_PLUGIN_ROOT") + "|" + cwd}}}, nil
	})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestClaudePluginRootCWDAndEnvironmentReachSubprocess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "claude")
	manifest := fmt.Sprintf(`{"name":"claude","mcpServers":{"env":{"command":%q,"args":["-test.run=^TestClaudePluginHelperProcess$"],"cwd":"${CLAUDE_PLUGIN_ROOT}","env":{"CAELIS_CLAUDE_ENV_HELPER":"1","CLAUDE_PLUGIN_ROOT":"/package-override"}}}}`, os.Args[0])
	writeAgentPluginFile(t, root, ".claude-plugin/plugin.json", manifest)
	contributions, err := ResolveContributions([]Config{{ID: "claude", Root: root, Enabled: true}})
	realRoot, rootErr := filepath.EvalSymlinks(root)
	if err != nil || rootErr != nil || len(contributions.MCPServerSpecs) != 1 ||
		contributions.MCPServerSpecs[0].WorkDir != realRoot || contributions.MCPServerSpecs[0].Env["CLAUDE_PLUGIN_ROOT"] != realRoot {
		t.Fatalf("Claude assembly = %+v, %v, %v", contributions.MCPServerSpecs, err, rootErr)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	mgr, err := mcp.NewManager(ctx, contributions.MCPServerSpecs, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	select {
	case <-mgr.Initialized():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var envTool tool.Tool
	for _, candidate := range mgr.Tools() {
		if candidate.Definition().Name == "env__environment" {
			envTool = candidate
		}
	}
	if envTool == nil {
		t.Fatalf("Claude environment tool not ready: %+v", mgr.GetServerInfos("claude"))
	}
	result, err := envTool.Call(ctx, tool.Call{ID: "env-call", Name: envTool.Definition().Name, Input: []byte(`{}`)})
	if err != nil || result.IsError || len(result.Content) != 1 || result.Content[0].Text == nil ||
		result.Content[0].Text.Text != realRoot+"|"+realRoot {
		t.Fatalf("Claude child environment = %+v, %v", result, err)
	}
}

func TestClaudePluginCommandStaysWithinRootThroughContributions(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "claude")
	outside := filepath.Join(parent, "outside")
	writeAgentPluginFile(t, parent, "outside", "outside")
	writeAgentPluginFile(t, root, "bin/good", "inside")
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	symlinkErr := os.Symlink(outside, filepath.Join(root, "bin", "escape"))
	cases := []struct {
		name, command string
		inline        bool
		wantServer    bool
		wantCommand   string
	}{
		{"parent", "../outside", true, false, ""},
		{"backslash-parent", `..\outside`, false, false, ""},
		{"root-variable-parent", "${CLAUDE_PLUGIN_ROOT}/../outside", true, false, ""},
		{"symlink", "bin/escape", false, false, ""},
		{"dot-relative-inside", "./bin/good", false, true, filepath.Join(realRoot, "bin", "good")},
		{"relative-inside", "bin/good", true, true, filepath.Join(realRoot, "bin", "good")},
		{"backslash-inside", `bin\good`, false, true, filepath.Join(realRoot, "bin", "good")},
		{"root-variable-inside", "${CLAUDE_PLUGIN_ROOT}/bin/good", false, true, filepath.Join(realRoot, "bin", "good")},
		{"explicit-absolute", outside, true, true, outside},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "symlink" && symlinkErr != nil {
				t.Skipf("symlink escape case unavailable: %v", symlinkErr)
			}
			server := fmt.Sprintf(`{"entry":{"command":%q}}`, tc.command)
			manifest := `{"name":"claude"}`
			mcpFile := `{"mcpServers":` + server + `}`
			if tc.inline {
				manifest = `{"name":"claude","mcpServers":` + server + `}`
				mcpFile = `{"mcpServers":{}}`
			}
			writeAgentPluginFile(t, root, ".claude-plugin/plugin.json", manifest)
			writeAgentPluginFile(t, root, ".mcp.json", mcpFile)
			contributions, err := ResolveContributions([]Config{{ID: "claude", Root: root, Enabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			if tc.wantServer {
				wantCount = 1
			}
			if got := len(contributions.MCPServerSpecs); got != wantCount {
				t.Fatalf("command %q: accepted %d servers; want %d", tc.command, got, wantCount)
			}
			if tc.wantServer && contributions.MCPServerSpecs[0].Command != tc.wantCommand {
				t.Fatalf("command %q resolved to %q, want %q", tc.command, contributions.MCPServerSpecs[0].Command, tc.wantCommand)
			}
		})
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

func TestAgentPluginHostExecutableEnvTargetOSPriority(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lite")
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"lite"}`)
	writeAgentPluginFile(t, root, "mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"desktop-world":{"type":"stdio","command":"./bin/dtw","env":{"dtw_node_path":"/package-choice"}}}}`)
	writeAgentPluginFile(t, root, "bin/dtw", "launcher")
	trusted := filepath.Join(t.TempDir(), "node")
	writeAgentPluginFile(t, filepath.Dir(trusted), filepath.Base(trusted), "host executable")
	realTrusted, err := filepath.EvalSymlinks(trusted)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{ID: "lite", Root: root, Enabled: true, ExecutableEnv: map[string]string{"DTW_NODE_PATH": trusted}}
	for _, tc := range []struct {
		os              string
		wantPackageCase bool
	}{
		{"windows", false},
		{"linux", true},
	} {
		t.Run(tc.os, func(t *testing.T) {
			got, err := ResolveContributions([]Config{config}, RuntimePaths{StoreDir: t.TempDir(), TargetOS: tc.os})
			if err != nil || len(got.MCPServerSpecs) != 1 {
				t.Fatalf("assembly = %+v, %v", got, err)
			}
			env := got.MCPServerSpecs[0].Env
			if env["DTW_NODE_PATH"] != realTrusted {
				t.Fatalf("Host selection missing: %+v", env)
			}
			_, hasPackageCase := env["dtw_node_path"]
			if hasPackageCase != tc.wantPackageCase {
				t.Fatalf("package case retained=%v, want %v: %+v", hasPackageCase, tc.wantPackageCase, env)
			}
		})
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
