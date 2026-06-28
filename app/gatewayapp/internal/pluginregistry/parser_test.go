package pluginregistry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/plugin"
)

func TestResolveSafePath(t *testing.T) {
	tmp, err := os.MkdirTemp("", "caelis-plugin-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	root := filepath.Join(tmp, "my-plugin")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatalf("failed to create plugin root: %v", err)
	}

	// Safe relative paths
	safe, err := ResolveSafePath(root, "skills")
	if err != nil {
		t.Errorf("ResolveSafePath(skills) failed: %v", err)
	}
	wantSafe := filepath.Join(root, "skills")
	if safe != wantSafe {
		t.Errorf("ResolveSafePath(skills) = %q, want %q", safe, wantSafe)
	}

	// Traversal escapes
	_, err = ResolveSafePath(root, "../sibling")
	if err == nil {
		t.Error("expected error for path traversal escape, got nil")
	}

	// Symlink escape test
	externalDir := filepath.Join(tmp, "external")
	if err := os.MkdirAll(externalDir, 0700); err != nil {
		t.Fatalf("failed to create external dir: %v", err)
	}
	symlinkPath := filepath.Join(root, "bad-symlink")
	if err := os.Symlink(externalDir, symlinkPath); err != nil {
		t.Logf("skipping symlink escape test: os.Symlink not supported: %v", err)
	} else {
		_, err = ResolveSafePath(root, "bad-symlink")
		if err == nil {
			t.Error("expected error for symlink escape, got nil")
		}
	}

	// Internal safe symlink test
	internalTarget := filepath.Join(root, "skills")
	if err := os.MkdirAll(internalTarget, 0700); err != nil {
		t.Fatalf("failed to create skills dir: %v", err)
	}
	goodSymlinkPath := filepath.Join(root, "good-symlink")
	if err := os.Symlink(internalTarget, goodSymlinkPath); err == nil {
		resolved, err := ResolveSafePath(root, "good-symlink")
		if err != nil {
			t.Errorf("ResolveSafePath on internal symlink failed: %v", err)
		}
		expectedResolved, _ := filepath.EvalSymlinks(internalTarget)
		if filepath.Clean(resolved) != filepath.Clean(expectedResolved) {
			t.Errorf("expected resolved internal symlink to be %q, got %q", expectedResolved, resolved)
		}
	}

	// Symlink escape test with nonexistent child path
	symlinkChildPath := filepath.Join(root, "bad-symlink-child")
	if err := os.Symlink(externalDir, symlinkChildPath); err == nil {
		_, err = ResolveSafePath(root, "bad-symlink-child/nonexistent-child")
		if err == nil {
			t.Error("expected error for symlink escape with nonexistent child path, got nil")
		}
	} else {
		t.Logf("skipping nonexistent-child symlink escape test: os.Symlink not supported: %v", err)
	}
}

func TestParseCaelisPlugin(t *testing.T) {
	tmp, err := os.MkdirTemp("", "caelis-plugin-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	root := filepath.Join(tmp, "test-caelis")
	metaDir := filepath.Join(root, ".caelis-plugin")
	if err := os.MkdirAll(metaDir, 0700); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}

	manifest := `{
		"name": "Test Caelis Plugin",
		"version": "1.2.3",
		"description": "A test native plugin",
		"skills": [
			{"root": "custom-skills", "namespace": "testns"}
		],
		"hooks": {
			"SessionStart": [
				{"command": "hooks/start.sh", "args": ["--debug"]}
			]
		},
		"mcpServers": {
			"myserver": {
				"command": "node",
				"args": ["index.js"]
			}
		}
	}`

	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	p, err := ParsePlugin(root)
	if err != nil {
		t.Fatalf("ParsePlugin failed: %v", err)
	}

	if p.Name != "Test Caelis Plugin" {
		t.Errorf("Name = %q, want %q", p.Name, "Test Caelis Plugin")
	}
	if p.Kind != plugin.ManifestKindCaelis {
		t.Errorf("Kind = %q, want %q", p.Kind, plugin.ManifestKindCaelis)
	}
	if len(p.Skills) != 1 || p.Skills[0].Namespace != "testns" {
		t.Errorf("unexpected skills: %+v", p.Skills)
	}
	if len(p.Hooks) != 1 || p.Hooks[0].Event != plugin.HookEventSessionStart {
		t.Errorf("unexpected hooks: %+v", p.Hooks)
	}
	if len(p.MCPServers) != 1 || p.MCPServers[0].Name != "myserver" {
		t.Errorf("unexpected MCP servers: %+v", p.MCPServers)
	}
	if p.MCPServers[0].Transport != plugin.MCPTransportStdio {
		t.Errorf("MCP transport = %q, want %q", p.MCPServers[0].Transport, plugin.MCPTransportStdio)
	}
}

func TestParseClaudePlugin(t *testing.T) {
	tmp, err := os.MkdirTemp("", "caelis-plugin-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	root := filepath.Join(tmp, "test-claude")
	metaDir := filepath.Join(root, ".claude-plugin")
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(metaDir, 0700); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}
	if err := os.MkdirAll(hooksDir, 0700); err != nil {
		t.Fatalf("failed to create hooks dir: %v", err)
	}

	manifest := `{
		"name": "Test Claude Plugin",
		"version": "2.0.0",
		"description": "Claude Code plugin"
	}`

	hooks := `{
		"hooks": {
			"SessionStart": [
				{
					"matcher": ".*",
					"hooks": [
						{
							"type": "command",
							"command": "bash start.sh --foo"
						}
					]
				}
			]
		}
	}`

	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooks), 0600); err != nil {
		t.Fatalf("failed to write hooks: %v", err)
	}

	p, err := ParsePlugin(root)
	if err != nil {
		t.Fatalf("ParsePlugin failed: %v", err)
	}

	if p.Name != "Test Claude Plugin" {
		t.Errorf("Name = %q, want %q", p.Name, "Test Claude Plugin")
	}
	if p.Kind != plugin.ManifestKindClaude {
		t.Errorf("Kind = %q, want %q", p.Kind, plugin.ManifestKindClaude)
	}
	if len(p.Hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(p.Hooks))
	}
	h := p.Hooks[0]
	if h.Event != plugin.HookEventSessionStart || h.RawCommand != "bash start.sh --foo" {
		t.Errorf("unexpected hook contents: %+v", h)
	}
}

func TestParseClaudePluginMalformedHooks(t *testing.T) {
	tmp, err := os.MkdirTemp("", "caelis-plugin-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	root := filepath.Join(tmp, "test-claude-malformed")
	metaDir := filepath.Join(root, ".claude-plugin")
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(metaDir, 0700); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}
	if err := os.MkdirAll(hooksDir, 0700); err != nil {
		t.Fatalf("failed to create hooks dir: %v", err)
	}

	manifest := `{
		"name": "Test Claude Plugin",
		"version": "2.0.0",
		"description": "Claude Code plugin"
	}`

	malformedHooks := `{
		"hooks": {
			"SessionStart": [
				{
					"matcher": ".*",
					"hooks": "not-a-list"
				}
			]
		}
	}`

	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(malformedHooks), 0600); err != nil {
		t.Fatalf("failed to write malformed hooks: %v", err)
	}

	_, err = ParsePlugin(root)
	if err == nil {
		t.Fatal("expected error parsing malformed hooks.json, got nil")
	}
	if !strings.Contains(err.Error(), "decode hooks.json") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseCodexPluginContributions(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "codex-demo")
	if err := os.MkdirAll(filepath.Join(root, ".codex-plugin"), 0o700); err != nil {
		t.Fatalf("mkdir codex manifest dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "plan"), 0o700); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o700); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	manifest := `{
		"name": "codex-demo",
		"version": "1.0.0",
		"description": "Codex demo",
		"skills": "./skills",
		"mcpServers": {
			"manifest-server": {"command": "node", "args": ["server.js"]}
		},
		"agents": [
			{"name": "helper", "command": "helper-bin", "workDir": "."}
		]
	}`
	if err := os.WriteFile(filepath.Join(root, ".codex-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	hooks := `{
		"hooks": {
			"SessionStart": [
				{"hooks": [{"type": "command", "command": "bash hooks/start.sh"}]}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(root, "hooks", "hooks.json"), []byte(hooks), 0o600); err != nil {
		t.Fatalf("write hooks: %v", err)
	}
	mcpJSON := `{
		"mcpServers": {
			"file-server": {"command": "python", "args": ["mcp.py"], "workDir": "."}
		}
	}`
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(mcpJSON), 0o600); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	p, err := ParsePlugin(root)
	if err != nil {
		t.Fatalf("ParsePlugin() error = %v", err)
	}
	if p.Kind != plugin.ManifestKindCodex {
		t.Fatalf("Kind = %q, want codex", p.Kind)
	}
	if len(p.Skills) != 1 || p.Skills[0].Root != filepath.Join(root, "skills") || p.Skills[0].Namespace != "codex-demo" {
		t.Fatalf("Skills = %#v, want codex namespaced skills root", p.Skills)
	}
	if len(p.Hooks) != 1 || p.Hooks[0].Command != "bash" || !strings.Contains(strings.Join(p.Hooks[0].Args, " "), "hooks/start.sh") {
		t.Fatalf("Hooks = %#v, want hook from hooks/hooks.json", p.Hooks)
	}
	if len(p.MCPServers) != 2 {
		t.Fatalf("MCPServers = %#v, want manifest and .mcp.json servers", p.MCPServers)
	}
	if len(p.Agents) != 1 || p.Agents[0].Name != "helper" || p.Agents[0].WorkDir != root {
		t.Fatalf("Agents = %#v, want helper agent rooted at plugin", p.Agents)
	}
}

func TestParseCodexPluginPathContributionFields(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "codex-paths")
	if err := os.MkdirAll(filepath.Join(root, ".codex-plugin"), 0o700); err != nil {
		t.Fatalf("mkdir codex manifest dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o700); err != nil {
		t.Fatalf("mkdir default hooks dir: %v", err)
	}
	manifest := `{
		"name": "codex-paths",
		"version": "1.0.0",
		"hooks": "config/hooks.json",
		"mcpServers": "config/mcp.json"
	}`
	if err := os.WriteFile(filepath.Join(root, ".codex-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	pathHooks := `{
		"hooks": {
			"SessionStart": [
				{"hooks": [{"type": "command", "command": "bash config/start.sh"}]}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(root, "config", "hooks.json"), []byte(pathHooks), 0o600); err != nil {
		t.Fatalf("write path hooks: %v", err)
	}
	defaultHooks := `{
		"hooks": {
			"SessionStart": [
				{"hooks": [{"type": "command", "command": "bash hooks/default.sh"}]}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(root, "hooks", "hooks.json"), []byte(defaultHooks), 0o600); err != nil {
		t.Fatalf("write default hooks: %v", err)
	}
	pathMCP := `{
		"servers": {
			"path-server": {"command": "python", "args": ["config/mcp.py"]}
		}
	}`
	if err := os.WriteFile(filepath.Join(root, "config", "mcp.json"), []byte(pathMCP), 0o600); err != nil {
		t.Fatalf("write path mcp: %v", err)
	}
	defaultMCP := `{
		"mcpServers": {
			"default-server": {"command": "node", "args": ["default.js"]}
		}
	}`
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(defaultMCP), 0o600); err != nil {
		t.Fatalf("write default mcp: %v", err)
	}

	p, err := ParsePlugin(root)
	if err != nil {
		t.Fatalf("ParsePlugin() error = %v", err)
	}
	if len(p.Hooks) != 1 || p.Hooks[0].Command != "bash" || !strings.Contains(strings.Join(p.Hooks[0].Args, " "), "config/start.sh") {
		t.Fatalf("Hooks = %#v, want only path hook", p.Hooks)
	}
	if len(p.MCPServers) != 1 || p.MCPServers[0].Name != "path-server" {
		t.Fatalf("MCPServers = %#v, want only path server", p.MCPServers)
	}
}

func TestParseCodexPluginRejectsEscapingSkillPath(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "codex-escape")
	if err := os.MkdirAll(filepath.Join(root, ".codex-plugin"), 0o700); err != nil {
		t.Fatalf("mkdir codex manifest dir: %v", err)
	}
	manifest := `{
		"name": "codex-escape",
		"skills": "../outside"
	}`
	if err := os.WriteFile(filepath.Join(root, ".codex-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	_, err := ParsePlugin(root)
	if err == nil {
		t.Fatal("ParsePlugin() error = nil, want escaping skill path rejection")
	}
	if !strings.Contains(err.Error(), "path traversal escape") {
		t.Fatalf("ParsePlugin() error = %v, want path traversal escape", err)
	}
}

func TestParseGeminiPlugin(t *testing.T) {
	tmp, err := os.MkdirTemp("", "caelis-plugin-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	root := filepath.Join(tmp, "test-gemini")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}

	manifest := `{
		"name": "superpowers",
		"version": "5.1.0",
		"description": "Gemini Extension",
		"mcpServers": {
			"server": {
				"command": "node",
				"args": ["server.js"]
			}
		}
	}`

	if err := os.WriteFile(filepath.Join(root, "gemini-extension.json"), []byte(manifest), 0600); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	p, err := ParsePlugin(root)
	if err != nil {
		t.Fatalf("ParsePlugin failed: %v", err)
	}

	if p.Name != "superpowers" {
		t.Errorf("Name = %q, want %q", p.Name, "superpowers")
	}
	if p.Kind != plugin.ManifestKindGemini {
		t.Errorf("Kind = %q, want %q", p.Kind, plugin.ManifestKindGemini)
	}
	if len(p.MCPServers) != 1 || p.MCPServers[0].Name != "server" {
		t.Errorf("unexpected MCP servers: %+v", p.MCPServers)
	}
}

func TestParseMCPRemoteTransports(t *testing.T) {
	tmp, err := os.MkdirTemp("", "caelis-plugin-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	root := filepath.Join(tmp, "test-remote-mcp")
	metaDir := filepath.Join(root, ".caelis-plugin")
	if err := os.MkdirAll(metaDir, 0700); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}

	manifest := `{
		"name": "remote-mcp",
		"version": "1.0.0",
		"mcpServers": {
			"httpServer": {
				"type": "http",
				"url": "https://example.test/mcp",
				"headers": {"Authorization": "Bearer token"}
			},
			"sseServer": {
				"transport": "sse",
				"url": "https://example.test/sse"
			}
		}
	}`

	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	p, err := ParsePlugin(root)
	if err != nil {
		t.Fatalf("ParsePlugin failed: %v", err)
	}
	servers := map[string]plugin.MCPServerSpec{}
	for _, server := range p.MCPServers {
		servers[server.Name] = server
	}
	if servers["httpServer"].Transport != plugin.MCPTransportStreamableHTTP {
		t.Errorf("httpServer transport = %q, want %q", servers["httpServer"].Transport, plugin.MCPTransportStreamableHTTP)
	}
	if servers["httpServer"].URL != "https://example.test/mcp" || servers["httpServer"].Headers["Authorization"] != "Bearer token" {
		t.Errorf("httpServer remote config not preserved: %+v", servers["httpServer"])
	}
	if servers["httpServer"].WorkDir != "" {
		t.Errorf("httpServer WorkDir = %q, want empty for remote transport", servers["httpServer"].WorkDir)
	}
	if servers["sseServer"].Transport != plugin.MCPTransportSSE {
		t.Errorf("sseServer transport = %q, want %q", servers["sseServer"].Transport, plugin.MCPTransportSSE)
	}
}

func TestParseCodexPlugin(t *testing.T) {
	tmp, err := os.MkdirTemp("", "caelis-plugin-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	root := filepath.Join(tmp, "test-codex")
	metaDir := filepath.Join(root, ".codex-plugin")
	if err := os.MkdirAll(metaDir, 0700); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}

	manifest := `{
		"name": "Test Codex Plugin",
		"version": "3.0.0",
		"description": "Codex plugin"
	}`

	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	p, err := ParsePlugin(root)
	if err != nil {
		t.Fatalf("ParsePlugin failed: %v", err)
	}

	if p.Name != "Test Codex Plugin" {
		t.Errorf("Name = %q, want %q", p.Name, "Test Codex Plugin")
	}
	if p.Kind != plugin.ManifestKindCodex {
		t.Errorf("Kind = %q, want %q", p.Kind, plugin.ManifestKindCodex)
	}
}

func TestParseMultiManifestMerging(t *testing.T) {
	tmp, err := os.MkdirTemp("", "caelis-plugin-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	root := filepath.Join(tmp, "test-multi")
	claudeMetaDir := filepath.Join(root, ".claude-plugin")
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(claudeMetaDir, 0700); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}
	if err := os.MkdirAll(hooksDir, 0700); err != nil {
		t.Fatalf("failed to create hooks dir: %v", err)
	}

	// Write Claude manifest
	claudeManifest := `{
		"name": "Claude superpowers",
		"version": "5.1.0",
		"description": "Claude superpowers version"
	}`
	hooks := `{
		"hooks": {
			"SessionStart": [
				{
					"matcher": ".*",
					"hooks": [
						{
							"type": "command",
							"command": "bash session-start.sh"
						}
					]
				}
			]
		}
	}`
	if err := os.WriteFile(filepath.Join(claudeMetaDir, "plugin.json"), []byte(claudeManifest), 0600); err != nil {
		t.Fatalf("failed to write claude manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooks), 0600); err != nil {
		t.Fatalf("failed to write hooks: %v", err)
	}

	// Write Gemini manifest
	geminiManifest := `{
		"name": "Gemini superpowers",
		"version": "5.1.0",
		"description": "Gemini superpowers version",
		"mcpServers": {
			"myserver": {
				"command": "node",
				"args": ["server.js"]
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(root, "gemini-extension.json"), []byte(geminiManifest), 0600); err != nil {
		t.Fatalf("failed to write gemini manifest: %v", err)
	}

	p, err := ParsePlugin(root)
	if err != nil {
		t.Fatalf("ParsePlugin failed: %v", err)
	}

	// Kind can be Claude or Gemini, but both contributions should be merged.
	if len(p.Hooks) != 1 || p.Hooks[0].RawCommand != "bash session-start.sh" {
		t.Errorf("lost Claude SessionStart hook: %+v", p.Hooks)
	}
	if len(p.MCPServers) != 1 || p.MCPServers[0].Name != "myserver" {
		t.Errorf("lost Gemini MCP server: %+v", p.MCPServers)
	}
}

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		input   string
		cmd     string
		args    []string
		wantErr bool
	}{
		{
			input: `bash -lc "echo hi"`,
			cmd:   "bash",
			args:  []string{"-lc", "echo hi"},
		},
		{
			input: `node server.js`,
			cmd:   "node",
			args:  []string{"server.js"},
		},
		{
			input: `python -c 'print("hello")'`,
			cmd:   "python",
			args:  []string{"-c", `print("hello")`},
		},
		{
			input:   `bash -c "unclosed quote`,
			wantErr: true,
		},
		{
			input:   `trailing backslash \`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		cmd, args, err := SplitCommand(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("SplitCommand(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr {
			if cmd != tt.cmd {
				t.Errorf("SplitCommand(%q) cmd = %q, want %q", tt.input, cmd, tt.cmd)
			}
			if len(args) != len(tt.args) {
				t.Errorf("SplitCommand(%q) args = %v, want %v", tt.input, args, tt.args)
			} else {
				for i, arg := range args {
					if arg != tt.args[i] {
						t.Errorf("SplitCommand(%q) args[%d] = %q, want %q", tt.input, i, arg, tt.args[i])
					}
				}
			}
		}
	}
}

func TestWorkDirNormalization(t *testing.T) {
	tmp, err := os.MkdirTemp("", "caelis-plugin-workdir-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	root := filepath.Join(tmp, "my-plugin")
	metaDir := filepath.Join(root, ".caelis-plugin")
	if err := os.MkdirAll(metaDir, 0700); err != nil {
		t.Fatalf("failed to create plugin root: %v", err)
	}

	manifest := `{
		"name": "Test Plugin",
		"version": "1.0.0",
		"hooks": {
			"SessionStart": [
				{"command": "echo", "workDir": "sub-dir"}
			]
		},
		"mcpServers": {
			"server1": {
				"command": "node",
				"workDir": ""
			}
		}
	}`

	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	// Create sub-dir to avoid resolve errors
	if err := os.MkdirAll(filepath.Join(root, "sub-dir"), 0700); err != nil {
		t.Fatalf("failed to create sub-dir: %v", err)
	}

	p, err := ParsePlugin(root)
	if err != nil {
		t.Fatalf("ParsePlugin failed: %v", err)
	}

	// Hook workDir should be normalized
	if len(p.Hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(p.Hooks))
	}
	expectedHookDir := filepath.Join(root, "sub-dir")
	if p.Hooks[0].WorkDir != expectedHookDir {
		t.Errorf("Hook WorkDir = %q, want %q", p.Hooks[0].WorkDir, expectedHookDir)
	}

	// MCP workDir should default to root if empty
	if len(p.MCPServers) != 1 {
		t.Fatalf("expected 1 MCP server, got %d", len(p.MCPServers))
	}
	if p.MCPServers[0].WorkDir != root {
		t.Errorf("MCP WorkDir = %q, want %q", p.MCPServers[0].WorkDir, root)
	}
}
