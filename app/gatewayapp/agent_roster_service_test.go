package gatewayapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func TestResolveACPConnectionLauncherUsesExistingGlobalAdapter(t *testing.T) {
	binDir := t.TempDir()
	bin := writeAgentRosterExecutable(t, binDir, "claude-agent-acp")
	t.Setenv("PATH", binDir)
	previous := runGlobalACPAgentInstall
	previousMatches := installedACPAdapterPackageMatches
	t.Cleanup(func() {
		runGlobalACPAgentInstall = previous
		installedACPAdapterPackageMatches = previousMatches
	})
	installedACPAdapterPackageMatches = func(string, builtinACPAdapterPackage) bool { return true }
	installCalls := 0
	runGlobalACPAgentInstall = func(context.Context, globalACPAgentInstallRequest) error {
		installCalls++
		return nil
	}

	connection, err := (&Stack{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
		AdapterID: "claude", Launcher: controlagents.LauncherChoiceGlobal,
	})
	if err != nil {
		t.Fatalf("resolveACPConnectionLauncher() error = %v", err)
	}
	if connection.Launcher.Command != bin || connection.Launcher.Kind != controlagents.LaunchKindExecutable {
		t.Fatalf("connection launcher = %#v, want existing global %q", connection.Launcher, bin)
	}
	if installCalls != 0 {
		t.Fatalf("global install calls = %d, want none", installCalls)
	}
}

func TestResolveACPConnectionLauncherInstallsMissingGlobalAdapter(t *testing.T) {
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	previous := runGlobalACPAgentInstall
	previousMatches := installedACPAdapterPackageMatches
	t.Cleanup(func() {
		runGlobalACPAgentInstall = previous
		installedACPAdapterPackageMatches = previousMatches
	})
	var gotSpec string
	installed := false
	installedACPAdapterPackageMatches = func(string, builtinACPAdapterPackage) bool { return installed }
	runGlobalACPAgentInstall = func(_ context.Context, req globalACPAgentInstallRequest) error {
		gotSpec = req.InstallSpec
		writeAgentRosterExecutable(t, binDir, "codex-acp")
		installed = true
		return nil
	}

	connection, err := (&Stack{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
		AdapterID: "codex", Launcher: controlagents.LauncherChoiceGlobal,
	})
	if err != nil {
		t.Fatalf("resolveACPConnectionLauncher() error = %v", err)
	}
	if gotSpec != "@agentclientprotocol/codex-acp@1.1.2" {
		t.Fatalf("global install spec = %q, want curated codex adapter", gotSpec)
	}
	if !strings.HasSuffix(connection.Launcher.Command, string(filepath.Separator)+"codex-acp") {
		t.Fatalf("connection launcher = %#v, want installed codex-acp", connection.Launcher)
	}
}

func TestResolveACPConnectionLauncherGlobalInstallFailureDoesNotFallbackToNPX(t *testing.T) {
	binDir := t.TempDir()
	writeAgentRosterExecutable(t, binDir, "npx")
	t.Setenv("PATH", binDir)
	previous := runGlobalACPAgentInstall
	previousMatches := installedACPAdapterPackageMatches
	t.Cleanup(func() {
		runGlobalACPAgentInstall = previous
		installedACPAdapterPackageMatches = previousMatches
	})
	installedACPAdapterPackageMatches = func(string, builtinACPAdapterPackage) bool { return false }
	runGlobalACPAgentInstall = func(context.Context, globalACPAgentInstallRequest) error {
		return errors.New("permission denied")
	}

	connection, err := (&Stack{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
		AdapterID: "claude", Launcher: controlagents.LauncherChoiceGlobal,
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("resolveACPConnectionLauncher() error = %v, want global install failure", err)
	}
	if connection.Launcher.Command != "" {
		t.Fatalf("connection launcher = %#v, must not fall back to npx", connection.Launcher)
	}
}

func TestResolveACPConnectionLauncherUsesInstalledNativePreset(t *testing.T) {
	binDir := t.TempDir()
	bin := writeAgentRosterExecutable(t, binDir, "opencode")
	t.Setenv("PATH", binDir)

	connection, err := (&Stack{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
		AdapterID: "opencode", Launcher: controlagents.LauncherChoiceInstalled,
	})
	if err != nil {
		t.Fatalf("resolveACPConnectionLauncher() error = %v", err)
	}
	if connection.ID != "opencode" || connection.Launcher.Command != bin || connection.Launcher.Kind != controlagents.LaunchKindExecutable {
		t.Fatalf("connection = %#v, want installed OpenCode executable", connection)
	}
	if got, want := connection.Launcher.Args, []string{"acp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("launcher args = %#v, want %#v", got, want)
	}
}

func TestResolveACPConnectionLauncherRejectsMissingNativePreset(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := (&Stack{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
		AdapterID: "grok", Launcher: controlagents.LauncherChoiceInstalled,
	})
	if err == nil || !strings.Contains(err.Error(), `"grok" is available on PATH`) {
		t.Fatalf("resolveACPConnectionLauncher() error = %v, want actionable PATH error", err)
	}
}

func TestManagedACPAdapterPackageMatchChecksPackageAndVersion(t *testing.T) {
	root := t.TempDir()
	pkg := builtinACPAdapterPackage{Package: "@agentclientprotocol/codex-acp", Version: "1.1.2", Bin: "codex-acp"}
	dir := filepath.Join(root, "node_modules", "@agentclientprotocol", "codex-acp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@agentclientprotocol/codex-acp","version":"1.1.2"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if !managedACPAdapterPackageMatches(root, pkg) {
		t.Fatal("managedACPAdapterPackageMatches() = false for curated package")
	}
	pkg.Version = "1.1.3"
	if managedACPAdapterPackageMatches(root, pkg) {
		t.Fatal("managedACPAdapterPackageMatches() = true for stale version")
	}
	pkg.Package = "@zed-industries/codex-acp"
	if managedACPAdapterPackageMatches(root, pkg) {
		t.Fatal("managedACPAdapterPackageMatches() = true for wrong package provenance")
	}
}

func TestSplitACPCommandLinePreservesQuotedExecutableAndArguments(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "agent bins")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	wantCommand := writeAgentRosterExecutable(t, binDir, "custom acp")
	command, args, err := splitACPCommandLine(`"` + wantCommand + `" --mode "deep review"`)
	if err != nil {
		t.Fatalf("splitACPCommandLine() error = %v", err)
	}
	if command != wantCommand {
		t.Fatalf("command = %q, want %q", command, wantCommand)
	}
	if len(args) != 2 || args[0] != "--mode" || args[1] != "deep review" {
		t.Fatalf("args = %#v, want quoted argument preserved", args)
	}
}

func TestRosterNameValidationRejectsRuntimeAndRunAddresses(t *testing.T) {
	for _, name := range []string{
		"self", "local", "main", "kernel", "sandbox", "guardian", "reviewer", "status", "worker(lina)", "bad name",
	} {
		if !forbiddenRosterAgentID(name) {
			t.Fatalf("forbiddenRosterAgentID(%q) = false", name)
		}
	}
	for _, name := range []string{"opus", "worker-2", "deepseek-v4-pro", "mimo-v2-5-pro"} {
		if forbiddenRosterAgentID(name) {
			t.Fatalf("forbiddenRosterAgentID(%q) = true", name)
		}
	}
}

func TestConnectACPPersistsRosterWithoutLegacyAgentDualWrite(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	binDir := t.TempDir()
	command := writeAgentRosterExecutable(t, binDir, "custom-acp")
	req := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand, CommandLine: command,
		ModelID: "opus", ConfigValues: map[string]string{"reasoning_effort": "max"}, CWD: stack.Workspace.CWD,
	}
	connection, err := stack.resolveACPConnectionLauncher(context.Background(), req)
	if err != nil {
		t.Fatalf("resolveACPConnectionLauncher() error = %v", err)
	}
	snapshot := controlagents.DiscoverySnapshot{
		ConnectionID: connection.ID, LaunchFingerprint: controlagents.LaunchFingerprint(connection.Launcher), CWD: stack.Workspace.CWD, SelectedModelID: "opus",
		Models: []controlagents.RemoteModel{{ID: "opus", Name: "Opus"}},
		ConfigOptions: []controlagents.ConfigOption{{
			ID: "reasoning_effort", Options: []controlagents.ConfigChoice{{Value: "max", Name: "Max"}},
		}},
	}
	req.Discovery = &snapshot
	result, err := stack.ConnectACP(context.Background(), req)
	if err != nil {
		t.Fatalf("ConnectACP() error = %v", err)
	}
	if len(result.Agents) != 1 || result.Agents[0].ID != "custom-acp" || result.Agents[0].Name != "custom-acp(opus)" {
		t.Fatalf("ConnectACP() result = %#v, want provider-first custom-acp Agent", result)
	}
	doc, err := LoadAppConfig(stack.storeDir)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	agent, _, err := controlagents.ResolveAgent(doc.AgentRoster, result.Agents[0].ID)
	if err != nil {
		t.Fatalf("ResolveAgent(%s) error = %v", result.Agents[0].ID, err)
	}
	if agent.Defaults.ModelID != "opus" || agent.Defaults.ConfigValues["reasoning_effort"] != "max" {
		t.Fatalf("persisted Agent = %#v, want model/reasoning defaults", agent)
	}
	if _, ok := storedACPAgentInfo(stack.ListACPAgents(), result.Agents[0].ID); !ok {
		t.Fatalf("ListACPAgents() = %#v, want materialized %s", stack.ListACPAgents(), result.Agents[0].ID)
	}
}

func TestConnectACPRollsBackPersistedRosterWhenAssemblyRefreshFails(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	command := writeAgentRosterExecutable(t, t.TempDir(), "rollback-acp")
	req := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand, CommandLine: command,
		ModelID: "opus", CWD: stack.Workspace.CWD,
	}
	connection, err := stack.resolveACPConnectionLauncher(context.Background(), req)
	if err != nil {
		t.Fatalf("resolveACPConnectionLauncher() error = %v", err)
	}
	req.Discovery = &controlagents.DiscoverySnapshot{
		ConnectionID: connection.ID, LaunchFingerprint: controlagents.LaunchFingerprint(connection.Launcher),
		CWD: stack.Workspace.CWD, SelectedModelID: "opus", Models: []controlagents.RemoteModel{{ID: "opus", Name: "Opus"}},
	}
	wantErr := errors.New("refresh failed")
	refreshCalls := 0
	stack.refreshConfiguredAgentsHook = func() error {
		refreshCalls++
		if refreshCalls == 1 {
			return wantErr
		}
		return nil
	}
	if _, err := stack.ConnectACP(context.Background(), req); !errors.Is(err, wantErr) {
		t.Fatalf("ConnectACP() error = %v, want %v", err, wantErr)
	}
	if refreshCalls != 2 {
		t.Fatalf("assembly refresh calls = %d, want failed apply plus rollback refresh", refreshCalls)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := controlagents.LookupConnection(doc.AgentRoster, connection.ID); ok {
		t.Fatalf("failed ConnectACP persisted connection %q", connection.ID)
	}
}

func storedACPAgentInfo(values []ACPAgentInfo, name string) (ACPAgentInfo, bool) {
	for _, value := range values {
		if strings.EqualFold(value.Name, name) {
			return value, true
		}
	}
	return ACPAgentInfo{}, false
}

func writeAgentRosterExecutable(t *testing.T, dir string, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("Abs(%s) error = %v", path, err)
	}
	return abs
}
