package mcpconfig

import (
	"os"
	"path/filepath"
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
