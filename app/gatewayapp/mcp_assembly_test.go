package gatewayapp

import (
	"os"
	"path/filepath"
	"testing"

	sdkmcp "github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/mcpconfig"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/workspacetrust"
)

func TestResolveConfiguredMCPServersIgnoresUntrustedProjectOverlay(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".agents"), 0o700); err != nil {
		t.Fatalf("mkdir home agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agents", "mcp.json"), []byte(`{
  "mcpServers": {
    "shared": {"command": "from-home"},
    "home-only": {"command": "home"}
  }
}`), 0o600); err != nil {
		t.Fatalf("write home mcp.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{
  "mcpServers": {
    "shared": {"command": "from-project"},
    "evil": {"command": "rm"}
  }
}`), 0o600); err != nil {
		t.Fatalf("write project .mcp.json: %v", err)
	}

	got, err := resolveConfiguredMCPServers(mcpconfig.Servers{
		"native": {Command: "from-config"},
	}, nil, workspace)
	if err != nil {
		t.Fatalf("resolveConfiguredMCPServers() error = %v", err)
	}
	byName := map[string]sdkmcp.ServerSpec{}
	for _, spec := range got {
		byName[spec.Name] = spec
	}
	if spec := byName["shared"]; spec.Command != "from-home" || spec.PluginID != mcpconfig.NamespaceUser {
		t.Fatalf("shared = %#v, want user overlay without project trust", spec)
	}
	if _, ok := byName["evil"]; ok {
		t.Fatalf("untrusted project overlay started %v", byName["evil"])
	}
	if spec := byName["native"]; spec.Command != "from-config" || spec.PluginID != mcpconfig.NamespaceUser {
		t.Fatalf("native = %#v", spec)
	}
}

func TestResolveConfiguredMCPServersUsesTrustedProjectOverlay(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".mcp.json"), []byte(`{
  "mcpServers": {
    "shared": {"command": "from-project"}
  }
}`), 0o600); err != nil {
		t.Fatalf("write project .mcp.json: %v", err)
	}

	got, err := resolveConfiguredMCPServers(mcpconfig.Servers{
		"shared": {Command: "from-config"},
	}, workspacetrust.Configuration{workspacetrust.NormalizeKey(workspace): workspacetrust.Trusted}, workspace)
	if err != nil {
		t.Fatalf("resolveConfiguredMCPServers() error = %v", err)
	}
	if len(got) != 1 || got[0].Command != "from-project" || got[0].PluginID != mcpconfig.NamespaceProject {
		t.Fatalf("got = %#v, want trusted project overlay", got)
	}
}

func TestLoadGatewayBuildPlanDoesNotSampleUntrustedProjectMCP(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	if err := os.WriteFile(filepath.Join(stack.composition.workspace.CWD, ".mcp.json"), []byte(`{
  "mcpServers": {
    "docs": {"command": "npx", "args": ["-y", "demo"]}
  }
}`), 0o600); err != nil {
		t.Fatalf("write project .mcp.json: %v", err)
	}

	plan, err := stack.composition.loadGatewayBuildPlan(stack.composition.sandbox, stack.composition.activeRuntime)
	if err != nil {
		t.Fatalf("loadGatewayBuildPlan() error = %v", err)
	}
	if len(plan.MCPServers) != 0 {
		t.Fatalf("plan.MCPServers = %#v, want no untrusted project overlay", plan.MCPServers)
	}
}

func TestWorkspaceTrustCommandEnablesProjectMCPOnLaterAssembly(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	workspace := stack.composition.workspace
	if err := os.WriteFile(filepath.Join(workspace.CWD, ".mcp.json"), []byte(`{
  "mcpServers": {
    "docs": {"command": "npx", "args": ["-y", "demo"]}
  }
}`), 0o600); err != nil {
		t.Fatalf("write project .mcp.json: %v", err)
	}
	revision, err := stack.composition.ConfigurationRevision(t.Context())
	if err != nil {
		t.Fatalf("ConfigurationRevision() error = %v", err)
	}
	result, err := stack.ConfigurationCommands().SetWorkspaceTrust(t.Context(), appserver.Principal{
		ID: stack.composition.authorities.userID,
	}, appserver.WorkspaceTrustRequest{
		WriteBase:    appserver.WriteBase{OperationID: "trust-workspace", ExpectedRevision: &revision},
		WorkspaceKey: workspace.Key,
		CWD:          workspace.CWD,
		TrustLevel:   workspacetrust.Trusted,
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision <= revision {
		t.Fatalf("SetWorkspaceTrust() = (%#v, %v)", result, err)
	}

	plan, err := stack.composition.loadGatewayBuildPlan(stack.composition.sandbox, stack.composition.activeRuntime)
	if err != nil {
		t.Fatalf("loadGatewayBuildPlan() error = %v", err)
	}
	if plan.ReleasePluginCache != nil {
		defer func() { _ = plan.ReleasePluginCache() }()
	}
	if len(plan.MCPServers) != 1 || plan.MCPServers[0].Name != "docs" || plan.MCPServers[0].PluginID != mcpconfig.NamespaceProject {
		t.Fatalf("plan.MCPServers = %#v, want trusted project server", plan.MCPServers)
	}

	doc, err := stack.composition.authorities.store.LoadContext(t.Context())
	if err != nil {
		t.Fatalf("LoadContext() error = %v", err)
	}
	if got := workspacetrust.Lookup(doc.WorkspaceTrust, workspace.CWD); got != workspacetrust.Trusted {
		t.Fatalf("persisted workspace trust = %q, want trusted", got)
	}
}

func TestUntrustedWorkspaceDoesNotParseProjectMCP(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	workspace := stack.composition.workspace
	if err := os.WriteFile(filepath.Join(workspace.CWD, ".mcp.json"), []byte(`{"mcpServers":`), 0o600); err != nil {
		t.Fatalf("write malformed project .mcp.json: %v", err)
	}
	doc, err := stack.composition.authorities.store.LoadContext(t.Context())
	if err != nil {
		t.Fatalf("LoadContext() error = %v", err)
	}
	doc.WorkspaceTrust, err = workspacetrust.Set(doc.WorkspaceTrust, workspace.CWD, workspacetrust.Untrusted)
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	plan, err := stack.composition.loadGatewayBuildPlan(stack.composition.sandbox, stack.composition.activeRuntime)
	if err != nil {
		t.Fatalf("loadGatewayBuildPlan() parsed untrusted project MCP: %v", err)
	}
	if plan.ReleasePluginCache != nil {
		defer func() { _ = plan.ReleasePluginCache() }()
	}
}

func TestMergeRuntimeMCPSpecsKeepsPluginNamespace(t *testing.T) {
	got, err := mergeRuntimeMCPSpecs(
		[]sdkmcp.ServerSpec{{PluginID: mcpconfig.NamespaceUser, Name: "docs", Command: "user"}},
		[]plugin.MCPServerSpec{{PluginID: "drawio", Name: "docs", Command: "plugin"}},
	)
	if err != nil {
		t.Fatalf("mergeRuntimeMCPSpecs() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("mergeRuntimeMCPSpecs() len = %d, want 2", len(got))
	}
	if got[0].PluginID != mcpconfig.NamespaceUser || got[1].PluginID != "drawio" {
		t.Fatalf("mergeRuntimeMCPSpecs() = %#v, want user then plugin identity", got)
	}
}

func TestMergeRuntimeMCPSpecsRejectsReservedPluginNamespace(t *testing.T) {
	_, err := mergeRuntimeMCPSpecs(
		nil,
		[]plugin.MCPServerSpec{{PluginID: mcpconfig.NamespaceUser, Name: "docs", Command: "plugin"}},
	)
	if err == nil {
		t.Fatal("mergeRuntimeMCPSpecs() error = nil, want reserved namespace")
	}
}
