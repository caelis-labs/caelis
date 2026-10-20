package mcpconfig

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdkmcp "github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func TestParseOverlayDocumentAcceptsWorkDirCamelCase(t *testing.T) {
	got, err := parseOverlayDocument([]byte(`{
  "mcpServers": {
    "docs": {
      "command": "npx",
      "args": ["-y", "demo"],
      "workDir": "/tmp/docs"
    }
  }
}`))
	if err != nil {
		t.Fatalf("parseOverlayDocument() error = %v", err)
	}
	if got["docs"].WorkDir != "/tmp/docs" {
		t.Fatalf("WorkDir = %q, want /tmp/docs", got["docs"].WorkDir)
	}
}

func TestMCPOverlayCannotClaimPluginSourceTrust(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "mcp.json")
	mustWrite(t, path, `{"mcpServers":{"desktop-world":{"command":"untrusted-mcp","plugin_id":"desktop-world","SourceTrusted":true,"source_trusted":true}}}`)
	got, err := Resolve(Request{UserAgentsFile: path})
	if err != nil || len(got) != 1 {
		t.Fatalf("Resolve() = %+v, %v", got, err)
	}
	if got[0].PluginID != NamespaceUser || got[0].SourceTrusted {
		t.Fatalf("MCP document self-authorized plugin trust: %+v", got[0])
	}
}

func TestProjectOverlayPresentChecksSupportedWorkspaceFiles(t *testing.T) {
	workspace := t.TempDir()
	mustMkdir(t, filepath.Join(workspace, ".mcp.json"))
	present, err := ProjectOverlayPresent(workspace)
	if err != nil || present {
		t.Fatalf("ProjectOverlayPresent(with directory) = (%v, %v), want false, nil", present, err)
	}

	mustMkdir(t, filepath.Join(workspace, ".agents"))
	mustWrite(t, filepath.Join(workspace, ".agents", "mcp.json"), `{"mcpServers":{}}`)
	present, err = ProjectOverlayPresent(workspace)
	if err != nil || !present {
		t.Fatalf("ProjectOverlayPresent(.agents/mcp.json) = (%v, %v), want true, nil", present, err)
	}

	otherWorkspace := t.TempDir()
	mustWrite(t, filepath.Join(otherWorkspace, ".mcp.json"), `{"mcpServers":{}}`)
	present, err = ProjectOverlayPresent(otherWorkspace)
	if err != nil || !present {
		t.Fatalf("ProjectOverlayPresent(.mcp.json) = (%v, %v), want true, nil", present, err)
	}
}

func TestResolveMergeOrderAndNamespaces(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	mustMkdir(t, filepath.Join(home, ".agents"))
	mustMkdir(t, filepath.Join(workspace, ".agents"))

	mustWrite(t, filepath.Join(home, ".agents", "mcp.json"), `{
  "mcpServers": {
    "shared": {"command": "from-agents-user", "args": ["a"]},
    "only-user": {"command": "user-only"}
  }
}`)
	mustWrite(t, filepath.Join(workspace, ".agents", "mcp.json"), `{
  "mcpServers": {
    "shared": {"command": "from-project-agents"}
  }
}`)
	mustWrite(t, filepath.Join(workspace, ".mcp.json"), `{
  "mcpServers": {
    "shared": {"command": "from-project-mcp"},
    "project-only": {"url": "https://mcp.example.com/mcp", "type": "http"}
  }
}`)

	nativeEnabled := false
	got, err := Resolve(Request{
		Native: Servers{
			"shared":    {Command: "from-config"},
			"native":    {Command: "native-only"},
			"only-user": {Enabled: &nativeEnabled},
		},
		UserAgentsFile:       filepath.Join(home, ".agents", "mcp.json"),
		ProjectAgentsFile:    filepath.Join(workspace, ".agents", "mcp.json"),
		ProjectMCPFile:       filepath.Join(workspace, ".mcp.json"),
		ProjectRoot:          workspace,
		AllowProjectOverlays: true,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	byName := map[string]sdkmcp.ServerSpec{}
	for _, spec := range got {
		byName[spec.Name] = spec
	}
	if spec := byName["shared"]; spec.Command != "from-project-mcp" || spec.PluginID != NamespaceProject {
		t.Fatalf("shared = %#v, want project overlay winner", spec)
	}
	if spec := byName["shared"]; !slices.Equal(spec.ReplaySourceIDs, []string{NamespaceUser, NamespaceProject}) {
		t.Fatalf("shared replay source IDs = %#v, want both catalog namespaces", spec.ReplaySourceIDs)
	}
	if spec := byName["native"]; spec.Command != "native-only" || spec.PluginID != NamespaceUser {
		t.Fatalf("native = %#v, want user catalog", spec)
	}
	if _, ok := byName["only-user"]; ok {
		t.Fatalf("only-user should be disabled by native catalog, got %#v", byName["only-user"])
	}
	if spec := byName["project-only"]; spec.URL != "https://mcp.example.com/mcp" || spec.Transport != sdkmcp.TransportStreamableHTTP {
		t.Fatalf("project-only = %#v", spec)
	}
	if spec := byName["shared"]; spec.WorkDir != workspace {
		t.Fatalf("shared WorkDir = %q, want workspace default", spec.WorkDir)
	}
}

func TestResolveSkipsProjectOverlaysUnlessAllowed(t *testing.T) {
	workspace := t.TempDir()
	mustWrite(t, filepath.Join(workspace, ".mcp.json"), `{
  "mcpServers": {
    "docs": {"command": "from-project"}
  }
}`)
	got, err := Resolve(Request{
		Native:         Servers{"docs": {Command: "from-config"}},
		ProjectMCPFile: filepath.Join(workspace, ".mcp.json"),
		ProjectRoot:    workspace,
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(got) != 1 || got[0].Command != "from-config" || got[0].PluginID != NamespaceUser {
		t.Fatalf("Resolve() = %#v, want native catalog without project overlay", got)
	}
}

func TestResolveAvailableIsolatesMalformedOverlayAndServer(t *testing.T) {
	root := t.TempDir()
	userFile := filepath.Join(root, "user.json")
	projectFile := filepath.Join(root, ".mcp.json")
	mustWrite(t, userFile, `{"mcpServers":{"user-good":{"command":"user"}}}`)
	mustWrite(t, projectFile, `{"mcpServers":`)
	req := Request{
		Native: Servers{
			"native-good": {Command: "native"}, "native-bad": {},
			strings.Repeat("x", 129): {Command: "too-long"},
		},
		UserAgentsFile: userFile, ProjectMCPFile: projectFile,
		ProjectRoot: root, AllowProjectOverlays: true,
	}
	if _, err := Resolve(req); err == nil {
		t.Fatal("strict Resolve accepted malformed overlay")
	}
	got, err := ResolveAvailable(req)
	if err != nil || len(got) != 2 || got[0].Name != "native-good" || got[1].Name != "user-good" {
		t.Fatalf("available servers = %+v, %v", got, err)
	}
	mustWrite(t, projectFile, `{"mcpServers":{"project-good":{"command":"project"},"project-bad":{"args":"wrong"}}}`)
	got, err = ResolveAvailable(req)
	if err != nil || len(got) != 3 || got[1].Name != "project-good" {
		t.Fatalf("available after file repair = %+v, %v", got, err)
	}
	mustWrite(t, projectFile, `{"mcpServers":{"project-good":{"command":"project"},"project-bad":{"command":"repaired"}}}`)
	got, err = ResolveAvailable(req)
	if err != nil || len(got) != 4 {
		t.Fatalf("available after server repair = %+v, %v", got, err)
	}
}

func TestResolveAvailableFallsBackBeforeHigherLayerCanShadow(t *testing.T) {
	root := t.TempDir()
	userFile := filepath.Join(root, "user", "mcp.json")
	projectAgents := filepath.Join(root, "workspace", ".agents", "mcp.json")
	projectMCP := filepath.Join(root, "workspace", ".mcp.json")
	workspace := filepath.Join(root, "workspace")
	mustWrite(t, userFile, `{"mcpServers":{"docs":{"command":"user"}}}`)
	mustWrite(t, projectAgents, `{"mcpServers":{"docs":{"command":"project-agents"}}}`)
	mustWrite(t, projectMCP, `{"mcpServers":{"docs":{}}}`)
	req := Request{
		Native: Servers{"docs": {Command: "native"}}, UserAgentsFile: userFile,
		ProjectAgentsFile: projectAgents, ProjectMCPFile: projectMCP,
		ProjectRoot: workspace, AllowProjectOverlays: true,
	}
	want := func(command, source string) {
		t.Helper()
		got, err := ResolveAvailable(req)
		if err != nil || len(got) != 1 || got[0].Command != command || got[0].PluginID != source {
			t.Fatalf("available docs = %+v, %v; want %s from %s", got, err, command, source)
		}
		combined, err := CombineSpecs(got, []sdkmcp.ServerSpec{{PluginID: "healthy-plugin", Name: "docs", Command: "plugin"}})
		if err != nil || len(combined) != 2 || combined[1].PluginID != "healthy-plugin" {
			t.Fatalf("plugin and catalog docs = %+v, %v", combined, err)
		}
	}
	want("project-agents", NamespaceProject) // Broken .mcp.json cannot shadow .agents/mcp.json.
	mustWrite(t, projectAgents, `{"mcpServers":{"docs":{}}}`)
	want("native", NamespaceUser) // Both broken workspace layers leave native intact.
	req.Native = Servers{"docs": {}}
	want("user", NamespaceUser) // Broken native item leaves healthy user overlay intact.
	mustWrite(t, projectMCP, `{"mcpServers":{"docs":{"enabled":false}}}`)
	req.Native = Servers{"docs": {Command: "native"}}
	got, err := ResolveAvailable(req)
	if err != nil || len(got) != 0 {
		t.Fatalf("explicit workspace disable must suppress lower docs: %+v, %v", got, err)
	}
	mustWrite(t, projectMCP, `{"mcpServers":{"docs":{"command":"repaired"}}}`)
	want("repaired", NamespaceProject)
}

func TestResolveAvailableDropsWholeNormalizedOverlayCollision(t *testing.T) {
	if _, err := parseOverlayDocument([]byte(`{"mcpServers":{"docs":{"command":"a"}," docs":{"command":"b"}}}`)); err == nil || !strings.Contains(err.Error(), "duplicate MCP server") {
		t.Fatalf("strict normalized collision error = %v", err)
	}
	for _, fixture := range []struct{ name, content string }{
		{"leading-space-first", `{"mcpServers":{" docs":{"command":"unexpected"},"docs":{"command":"expected"},"other":{"command":"other"},"   ":{"command":"empty"}}}`},
		{"leading-space-last", `{"mcpServers":{"docs":{"command":"expected"},"other":{"command":"other"}," docs":{"args":"malformed"},"   ":{"command":"empty"}}}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), ".mcp.json")
			mustWrite(t, file, fixture.content)
			if _, err := loadOverlayFile(file); err == nil {
				t.Fatal("strict overlay accepted malformed names or content")
			}
			available := loadAvailableOverlayFile(file)
			if len(available) != 1 || available["other"].Command != "other" {
				t.Fatalf("available overlay selected a colliding or empty name: %+v", available)
			}
			got, err := ResolveAvailable(Request{
				Native:         Servers{"docs": {Command: "safe-lower"}},
				ProjectMCPFile: file, ProjectRoot: filepath.Dir(file), AllowProjectOverlays: true,
			})
			if err != nil || len(got) != 2 || got[0].Name != "docs" || got[0].Command != "safe-lower" || got[1].Name != "other" {
				t.Fatalf("colliding project names changed lower source: %+v, %v", got, err)
			}
			user, err := ResolveAvailable(Request{UserAgentsFile: file})
			if err != nil || len(user) != 1 || user[0].Name != "other" {
				t.Fatalf("colliding user names selected a winner: %+v, %v", user, err)
			}
			combined, err := CombineSpecs(user, []sdkmcp.ServerSpec{{PluginID: "healthy-plugin", Name: "docs", Command: "plugin"}})
			if err != nil || len(combined) != 2 || combined[1].PluginID != "healthy-plugin" {
				t.Fatalf("user collision affected independent plugin: %+v, %v", combined, err)
			}
		})
	}
	_, err := ResolveAvailable(Request{Native: Servers{
		" docs": {Command: "a"}, "docs": {Command: "b"},
	}})
	if err == nil || !strings.Contains(err.Error(), "duplicate MCP server") {
		t.Fatalf("native identity collision must remain fatal: %v", err)
	}
}

func TestCombineSpecsRejectsDuplicatesAndReservedPluginIDs(t *testing.T) {
	_, err := CombineSpecs(
		[]sdkmcp.ServerSpec{{PluginID: NamespaceUser, Name: "docs", Command: "a"}},
		[]sdkmcp.ServerSpec{{PluginID: NamespaceUser, Name: "other", Command: "b"}},
	)
	if err == nil || !strings.Contains(err.Error(), "reserved MCP namespace") {
		t.Fatalf("CombineSpecs() error = %v, want reserved namespace", err)
	}
	_, err = CombineSpecs(
		[]sdkmcp.ServerSpec{
			{PluginID: NamespaceUser, Name: "docs", Command: "a"},
			{PluginID: NamespaceUser, Name: "docs", Command: "b"},
		},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate MCP server") {
		t.Fatalf("CombineSpecs() error = %v, want duplicate", err)
	}
}

func TestResolveSkipsMissingFiles(t *testing.T) {
	got, err := Resolve(Request{
		Native: Servers{
			"docs": {Command: "npx", URL: ""},
		},
		UserAgentsFile:    filepath.Join(t.TempDir(), "missing.json"),
		ProjectAgentsFile: filepath.Join(t.TempDir(), ".agents", "mcp.json"),
		ProjectMCPFile:    filepath.Join(t.TempDir(), ".mcp.json"),
		ProjectRoot:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(got) != 1 || got[0].Name != "docs" || got[0].PluginID != NamespaceUser {
		t.Fatalf("Resolve() = %#v", got)
	}
}

func TestResolveRejectsProjectWorkDirEscape(t *testing.T) {
	workspace := t.TempDir()
	mustWrite(t, filepath.Join(workspace, ".mcp.json"), `{
  "mcpServers": {
    "docs": {"command": "npx", "workDir": "../escape"}
  }
}`)
	_, err := Resolve(Request{
		ProjectMCPFile:       filepath.Join(workspace, ".mcp.json"),
		ProjectRoot:          workspace,
		AllowProjectOverlays: true,
	})
	if err == nil || !strings.Contains(err.Error(), "escapes project root") {
		t.Fatalf("Resolve() error = %v, want project root escape", err)
	}
}

func TestResolveRejectsProjectWorkDirSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(workspace, "outside")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	mustWrite(t, filepath.Join(workspace, ".mcp.json"), `{
  "mcpServers": {
    "docs": {"command": "npx", "workDir": "outside"}
  }
}`)
	_, err := Resolve(Request{
		ProjectMCPFile:       filepath.Join(workspace, ".mcp.json"),
		ProjectRoot:          workspace,
		AllowProjectOverlays: true,
	})
	if err == nil || !strings.Contains(err.Error(), "escapes project root") {
		t.Fatalf("Resolve() error = %v, want symlink escape rejection", err)
	}
}

func TestResolveInvalidOverlayFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")
	mustWrite(t, path, `{"mcpServers":`)
	_, err := Resolve(Request{ProjectMCPFile: path, ProjectRoot: t.TempDir(), AllowProjectOverlays: true})
	if err == nil {
		t.Fatal("Resolve() error = nil, want parse failure")
	}
}

func TestResolveHTTPDoesNotRequireWorkDir(t *testing.T) {
	got, err := Resolve(Request{
		Native: Servers{
			"remote": {Type: "http", URL: "https://mcp.example.com/mcp"},
		},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(got) != 1 || got[0].WorkDir != "" || got[0].Transport != sdkmcp.TransportStreamableHTTP {
		t.Fatalf("Resolve() = %#v", got)
	}
}

func TestResolveNativeOverridesUserAgentsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	mustWrite(t, path, `{
  "mcpServers": {
    "docs": {"command": "from-file", "env": {"A": "1"}}
  }
}`)
	got, err := Resolve(Request{
		Native:         Servers{"docs": {Command: "from-config"}},
		UserAgentsFile: path,
		ProjectRoot:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(got) != 1 || got[0].Command != "from-config" || got[0].PluginID != NamespaceUser {
		t.Fatalf("Resolve() = %#v", got)
	}
	if len(got[0].Env) != 0 {
		t.Fatalf("native overlay must replace, not merge fields: env=%v", got[0].Env)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
