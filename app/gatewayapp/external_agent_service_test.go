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
	"github.com/caelis-labs/caelis/control/modelprofile"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func TestResolveACPConnectionLauncherUsesExistingGlobalAdapter(t *testing.T) {
	binDir := t.TempDir()
	bin := writeExternalAgentExecutable(t, binDir, "claude-agent-acp")
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
		writeExternalAgentExecutable(t, binDir, "codex-acp")
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
	writeExternalAgentExecutable(t, binDir, "npx")
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
	bin := writeExternalAgentExecutable(t, binDir, "opencode")
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
	wantCommand := writeExternalAgentExecutable(t, binDir, "custom acp")
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
		if !forbiddenExternalAgentID(name) {
			t.Fatalf("forbiddenExternalAgentID(%q) = false", name)
		}
	}
	for _, name := range []string{"opus", "worker-2", "deepseek-v4-pro", "mimo-v2-5-pro"} {
		if forbiddenExternalAgentID(name) {
			t.Fatalf("forbiddenExternalAgentID(%q) = true", name)
		}
	}
}

func TestConnectACPPersistsRosterWithoutLegacyAgentDualWrite(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	binDir := t.TempDir()
	command := writeExternalAgentExecutable(t, binDir, "custom-acp")
	req := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand, CommandLine: command,
		ModelID: "opus", ConfigValues: map[string]string{"thought_level": "very-high"}, CWD: stack.Workspace.CWD,
	}
	connection, err := stack.resolveACPConnectionLauncher(context.Background(), req)
	if err != nil {
		t.Fatalf("resolveACPConnectionLauncher() error = %v", err)
	}
	snapshot := controlagents.DiscoverySnapshot{
		ConnectionID: connection.ID, LaunchFingerprint: controlagents.LaunchFingerprint(connection.Launcher), CWD: stack.Workspace.CWD, SelectedModelID: "opus",
		Models: []controlagents.RemoteModel{{ID: "opus", Name: "Opus"}},
		ConfigOptions: []controlagents.ConfigOption{{
			ID: "thought_level", Category: "reasoning", CurrentValue: "very-high",
			Options: []controlagents.ConfigChoice{{Value: "high", Name: "High"}, {Value: "very-high", Name: "Very High"}},
		}},
	}
	req.Discovery = &snapshot
	result, err := stack.ConnectACP(context.Background(), req)
	if err != nil {
		t.Fatalf("ConnectACP() error = %v", err)
	}
	if len(result.Profiles) != 1 || result.Profiles[0].Backend.ACP == nil || result.Profiles[0].Backend.ACP.RemoteModelID != "opus" {
		t.Fatalf("ConnectACP() result = %#v, want Opus ModelProfile", result)
	}
	doc, err := LoadAppConfig(stack.storeDir)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	agentID := result.Profiles[0].Backend.ACP.AgentID
	agent, _, err := controlagents.ResolveAgent(doc.ExternalAgents, agentID)
	if err != nil {
		t.Fatalf("ResolveAgent(%s) error = %v", agentID, err)
	}
	profile := result.Profiles[0]
	if agent.ConnectionID != result.Connection.ID || profile.Effort.ACPConfigID != "thought_level" || profile.Effort.DefaultEffort != "xhigh" {
		t.Fatalf("persisted Agent/profile = %#v %#v", agent, profile)
	}
	if wire, ok := profile.WireEffort("xhigh"); !ok || wire != "very-high" {
		t.Fatalf("WireEffort(xhigh) = %q, %v", wire, ok)
	}
	if _, ok := storedACPAgentInfo(stack.ListACPAgents(), agentID); !ok {
		t.Fatalf("ListACPAgents() = %#v, want materialized %s", stack.ListACPAgents(), agentID)
	}
}

func TestConnectACPRollsForwardAfterCommittedConfigWriteFault(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	command := writeExternalAgentExecutable(t, t.TempDir(), "committed-connect-acp")
	req := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand, CommandLine: command,
		ModelID: "opus", CWD: stack.Workspace.CWD,
	}
	connection, err := stack.resolveACPConnectionLauncher(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Discovery = &controlagents.DiscoverySnapshot{
		ConnectionID: connection.ID, LaunchFingerprint: controlagents.LaunchFingerprint(connection.Launcher),
		CWD: stack.Workspace.CWD, SelectedModelID: "opus", Models: []controlagents.RemoteModel{{ID: "opus", Name: "Opus"}},
	}
	fault := errors.New("chmod after rename failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "chmod", fault)

	result, err := stack.ConnectACP(context.Background(), req)
	requireCommittedConfigWriteError(t, err, fault)
	if len(result.Profiles) != 1 || writeCount() != 1 {
		t.Fatalf("ConnectACP() result/writes = %#v/%d, want one committed profile write", result, writeCount())
	}
	profile := result.Profiles[0]
	doc, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := controlagents.LookupAgent(doc.ExternalAgents, profile.Backend.ACP.AgentID); !ok {
		t.Fatalf("committed config is missing Agent %q", profile.Backend.ACP.AgentID)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, profile.ID); !ok {
		t.Fatalf("committed config is missing ModelProfile %q", profile.ID)
	}
	if _, ok := storedACPAgentInfo(stack.ListACPAgents(), profile.Backend.ACP.AgentID); !ok {
		t.Fatalf("runtime assembly is missing committed ACP Agent %q: %#v", profile.Backend.ACP.AgentID, stack.ListACPAgents())
	}
}

func TestConnectACPSiblingModelsShareOneAgentButKeepProfilesIsolated(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	command := writeExternalAgentExecutable(t, t.TempDir(), "sibling-acp")
	base := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand, CommandLine: command, CWD: stack.Workspace.CWD,
	}
	connection, err := stack.resolveACPConnectionLauncher(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	connect := func(modelID, tone, name string) modelprofile.ModelProfile {
		t.Helper()
		req := base
		req.ModelID = modelID
		req.ConfigValues = map[string]string{"tone": tone}
		req.Discovery = &controlagents.DiscoverySnapshot{
			ConnectionID: connection.ID, LaunchFingerprint: controlagents.LaunchFingerprint(connection.Launcher),
			CWD: stack.Workspace.CWD, SelectedModelID: modelID,
			Models: []controlagents.RemoteModel{{ID: modelID, Name: name}},
			ConfigOptions: []controlagents.ConfigOption{{
				ID: "tone", CurrentValue: tone,
				Options: []controlagents.ConfigChoice{{Value: "concise"}, {Value: "detailed"}},
			}},
		}
		result, connectErr := stack.ConnectACP(context.Background(), req)
		if connectErr != nil {
			t.Fatal(connectErr)
		}
		if len(result.Profiles) != 1 {
			t.Fatalf("ConnectACP(%s) profiles = %#v", modelID, result.Profiles)
		}
		return result.Profiles[0]
	}
	opus := connect("opus", "concise", "Opus")
	sonnet := connect("sonnet", "detailed", "Sonnet")
	reconnected := connect("opus", "concise", "Opus Updated")
	if opus.ID != reconnected.ID || opus.ID == sonnet.ID {
		t.Fatalf("profile identities = opus:%q reconnect:%q sonnet:%q", opus.ID, reconnected.ID, sonnet.ID)
	}
	if opus.Backend.ACP.AgentID != sonnet.Backend.ACP.AgentID ||
		opus.Backend.ACP.SessionDefaults["tone"] != "concise" ||
		sonnet.Backend.ACP.SessionDefaults["tone"] != "detailed" {
		t.Fatalf("sibling profiles = opus:%#v sonnet:%#v", opus, sonnet)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.ExternalAgents.Agents) != 1 || doc.ExternalAgents.Agents[0].ConnectionID != connection.ID {
		t.Fatalf("external Agent catalog = %#v", doc.ExternalAgents)
	}
	var acpProfiles int
	for _, profile := range doc.ModelProfiles.Profiles {
		if profile.Backend.ACP != nil {
			acpProfiles++
		}
	}
	if acpProfiles != 2 {
		t.Fatalf("persisted ModelProfiles = %#v, want two ACP siblings", doc.ModelProfiles.Profiles)
	}
}

func TestConnectACPRollsBackPersistedRosterWhenAssemblyRefreshFails(t *testing.T) {
	stack := newStackForToolTestWithoutProfiles(t, assembly.ResolvedAssembly{})
	command := writeExternalAgentExecutable(t, t.TempDir(), "rollback-acp")
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
	if _, ok := controlagents.LookupConnection(doc.ExternalAgents, connection.ID); ok {
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

func writeExternalAgentExecutable(t *testing.T, dir string, name string) string {
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
