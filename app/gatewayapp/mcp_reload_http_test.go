package gatewayapp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/control/workspacetrust"
)

func TestNativeMCPAutoReloadIsolatesBrokenFilesAndServersHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	root := t.TempDir()
	audit := filepath.Join(root, "mcp-audit")
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	t.Setenv("CAELIS_ATOMIC_MCP_AUDIT", audit)
	writeFile := func(path string, raw []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	server := func(testName string) map[string]any {
		return map[string]any{"command": os.Args[0], "args": []string{"-test.run=^" + testName + "$"}}
	}
	writeServers := func(path string, servers map[string]any) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"mcpServers": servers})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(path, raw)
	}
	writeServers(filepath.Join(home, ".agents", "mcp.json"), map[string]any{"documents": server("TestAtomicDocumentsMCPHelper")})

	provider := &atomicCapabilityProvider{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	hostStatus, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	workspace := host.stack.Workspace()
	if _, err := host.host.SetWorkspaceTrust(ctx, appserver.WorkspaceTrustRequest{
		WriteBase:    appserver.WriteBase{OperationID: "reload-trust", ExpectedRevision: &hostStatus.Configuration.Revision},
		WorkspaceKey: workspace.Key, CWD: workspace.CWD, TrustLevel: workspacetrust.Trusted,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := host.host.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "reload-create"}, CWD: workspace.CWD, WorkspaceKey: workspace.Key, Model: "custom/gpt-4.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := host.host.Reconnect(ctx, appserver.ReconnectRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	state, err := host.host.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.host.ConfigureSessionMode(ctx, appserver.SessionModeRequest{
		WriteBase: appserver.WriteBase{OperationID: "reload-manual", SessionID: created.SessionID, ExpectedRevision: &state.Revision},
		Mode:      "manual",
	}); err != nil {
		t.Fatal(err)
	}
	promptAtomicCapabilityClient(t, ctx, host.host, created.SessionID, "reload-before", "BASIC")
	waitAtomicApplicationMCPRunning(t, ctx, host.host, created.SessionID, "documents")
	if builtinComputerUseSupported() {
		waitBuiltinDesktopWorldMCP(t, ctx, host.host, created.SessionID)
		promptAtomicCapabilityClient(t, ctx, host.host, created.SessionID, "reload-baseline", "BASIC")
	}
	beforeTools := provider.lastTools()
	beforeAudit := atomicAudit(audit)
	systemPrompt := func() string {
		t.Helper()
		var request struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if err := json.Unmarshal([]byte(provider.lastRequest()), &request); err != nil {
			t.Fatal(err)
		}
		for _, message := range request.Messages {
			if message.Role == "system" || message.Role == "developer" {
				return string(message.Content)
			}
		}
		t.Fatal("model request is missing a system message")
		return ""
	}
	beforeSystem := systemPrompt()

	configPath := filepath.Join(root, "store", "config.json")
	hostConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(configPath, []byte(`{"mcp_servers":`))
	writeFile(filepath.Join(workspace.CWD, ".agents", "mcp.json"), []byte(`{"mcpServers":`))
	writeServers(filepath.Join(workspace.CWD, ".mcp.json"), map[string]any{
		"utilities": server("TestAtomicUtilitiesMCPHelper"),
		"failed":    server("TestAtomicFailedMCPHelper"),
		"invalid":   map[string]any{"command": 42},
		"dtw":       server("TestAtomicFailedMCPHelper"),
	})
	waitAtomicApplicationMCPRunning(t, ctx, host.host, created.SessionID, "utilities")
	waitAtomicAudit(t, audit, "failed:start:")
	writeFile(configPath, hostConfig)
	promptAtomicCapabilityClient(t, ctx, host.host, created.SessionID, "reload-after", "UTIL")
	if provider.lastTools() != beforeTools || systemPrompt() != beforeSystem {
		t.Fatal("MCP-only reload changed the model tool declarations or system message")
	}
	if got := atomicAudit(audit); strings.Count(got, "documents:start:") != strings.Count(beforeAudit, "documents:start:") ||
		strings.Count(got, "documents:stop:") != strings.Count(beforeAudit, "documents:stop:") ||
		strings.Count(got, "utilities:call:") != 1 || strings.Count(got, "failed:start:") != 1 {
		t.Fatalf("reload restarted the healthy server or missed the new tool call: %s", got)
	}
	status, err := host.host.SessionMCPStatus(ctx, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range status.Servers {
		if entry.Name == "invalid" || entry.Name == "failed" && entry.Ready {
			t.Fatalf("invalid or failed server entered the ready catalog: %+v", entry)
		}
	}
}

func TestApplicationInheritedCapabilitiesAreAdditiveAndReloadHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	root := t.TempDir()
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	audit := filepath.Join(root, "mcp-audit")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	t.Setenv("CAELIS_ATOMIC_MCP_AUDIT", audit)
	writeServer := func(path, name, helper string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{"mcpServers": map[string]any{name: map[string]any{
			"command": os.Args[0], "args": []string{"-test.run=^" + helper + "$"},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeServer(filepath.Join(home, ".agents", "mcp.json"), "documents", "TestAtomicDocumentsMCPHelper")
	writeAtomicSkill(t, filepath.Join(home, ".agents", "skills", "host"), "host-skill", "HOST_SKILL_BODY")
	writeAtomicSkill(t, filepath.Join(home, ".agents", "skills", "default"), "atomic-skill", "DEFAULT_ATOMIC_SKILL_BODY")
	projectSkills := filepath.Join(workspace, ".agents", "skills")
	writeAtomicSkill(t, filepath.Join(projectSkills, "existing"), "workspace-skill", "WORKSPACE_SKILL_BODY")
	explicitRoot := filepath.Join(root, "explicit")
	writeAtomicSkill(t, explicitRoot, "atomic-skill", "EXPLICIT_SKILL_BODY")

	provider := &atomicCapabilityProvider{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), workspace, provider)
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "app.credential"))
	profile := applicationHTTPProfile()
	profile.Workspace.CWD = host.stack.Workspace().CWD
	profile.SkillDirs = []string{explicitRoot}
	profile.MCPServers = []sessionprofile.MCPServer{{Name: "documents", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicDocumentsMCPHelper$"}, WorkDir: profile.Workspace.CWD}}
	created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "inherit-create"}, Profile: &profile})
	if err != nil {
		t.Fatal(err)
	}
	id := created.SessionID
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	promptAtomicCapabilityClient(t, ctx, client, id, "inherit-before", "BASIC")
	waitAtomicApplicationMCPRunning(t, ctx, client, id, "documents")
	if builtinComputerUseSupported() {
		waitBuiltinDesktopWorldMCP(t, ctx, client, id)
		promptAtomicCapabilityClient(t, ctx, client, id, "inherit-baseline", "BASIC")
	}
	before := atomicSystemInstructions(t, provider.lastRequest())
	beforeTools := provider.lastTools()
	for _, name := range []string{"host-skill", "workspace-skill", "atomic-skill"} {
		if !strings.Contains(before, name) {
			t.Fatalf("missing skill %s in %s", name, before)
		}
	}
	promptAtomicCapabilityClient(t, ctx, client, id, "inherit-explicit-skill", "SKILL")
	if got := provider.lastRequest(); !strings.Contains(got, "EXPLICIT_SKILL_BODY") || strings.Contains(got, "DEFAULT_ATOMIC_SKILL_BODY") {
		t.Fatal("explicit Skill did not take precedence over the inherited Skill")
	}
	verifyDocuments := func(wantInherited bool) {
		t.Helper()
		status, err := client.SessionMCPStatus(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(status.Servers, func(server sessionprofile.MCPServerStatus) bool {
			return server.Name == "documents" && server.Ready && (server.PluginID != "") == wantInherited
		}) {
			t.Fatalf("unexpected documents source: %+v", status.Servers)
		}
	}
	verifyDocuments(false)
	writeServer(filepath.Join(workspace, ".mcp.json"), "utilities", "TestAtomicUtilitiesMCPHelper")
	hostStatus, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	w := host.stack.Workspace()
	if _, err := host.host.SetWorkspaceTrust(ctx, appserver.WorkspaceTrustRequest{
		WriteBase:    appserver.WriteBase{OperationID: "inherit-trust", ExpectedRevision: &hostStatus.Configuration.Revision},
		WorkspaceKey: w.Key, CWD: w.CWD, TrustLevel: workspacetrust.Trusted,
	}); err != nil {
		t.Fatal(err)
	}
	waitAtomicApplicationMCPRunning(t, ctx, client, id, "utilities")
	if approvals := promptAtomicCapabilityClientWithMCPApproval(t, ctx, client, id, "inherit-call-new", "UTIL", "allow_once"); approvals != 1 {
		t.Fatalf("first tools-only call to a discovered workspace MCP server requested %d approvals, want 1", approvals)
	}
	writeAtomicSkill(t, filepath.Join(projectSkills, "reload"), "reload-skill", "INHERITED_RELOAD_BODY")
	promptAtomicCapabilityClient(t, ctx, client, id, "inherit-load-new", "RELOAD_SKILL")
	got := provider.lastRequest()
	if !strings.Contains(got, "INHERITED_RELOAD_BODY") {
		t.Fatal("model context lacks the live inherited Skill body")
	}
	if atomicSystemInstructions(t, got) != before {
		t.Fatal("live Skill loading changed the system instructions")
	}
	if tools := provider.lastTools(); tools != beforeTools {
		t.Fatalf("live capability reload changed tool declarations: got %s, want %s", tools, beforeTools)
	}
	emptyServers, emptyDirs := []sessionprofile.MCPServer{}, []string{}
	if _, err := configurationResult(client.UpdateSessionConfiguration(ctx, configurationCommand(id, sessionprofile.UpdateConfigurationRequest{
		OperationID: "inherit-clear", ExpectedConfigurationRevision: 1,
		Patch: sessionprofile.ConfigurationPatch{MCPServers: &emptyServers, SkillDirs: &emptyDirs},
	}))); err != nil {
		t.Fatal(err)
	}
	waitAtomicApplicationMCPRunning(t, ctx, client, id, "documents")
	verifyDocuments(true)
	promptAtomicCapabilityClient(t, ctx, client, id, "inherit-default-skill", "SKILL")
	if got := provider.lastRequest(); !strings.Contains(got, "DEFAULT_ATOMIC_SKILL_BODY") || atomicSystemInstructions(t, got) != before {
		t.Fatal("clearing explicit directories did not restore the default Skill under a stable prefix")
	}
	promptAtomicCapabilityClient(t, ctx, client, id, "inherit-after-clear", "UTIL")
	if got := atomicAudit(audit); strings.Count(got, "utilities:start:") != 1 || strings.Count(got, "utilities:call:") != 2 {
		t.Fatalf("explicit removal restarted or lost the inherited workspace server: %s", got)
	}
}
