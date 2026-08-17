package gatewayapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
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

	connection, err := (&controlCommandBackend{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
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
	installed := false
	var gotSpec string
	var installedBin string
	installedACPAdapterPackageMatches = func(string, builtinACPAdapterPackage) bool { return installed }
	runGlobalACPAgentInstall = func(_ context.Context, req globalACPAgentInstallRequest) error {
		gotSpec = req.InstallSpec
		installedBin = writeExternalAgentExecutable(t, binDir, "codex-acp")
		installed = true
		return nil
	}

	connection, err := (&controlCommandBackend{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
		AdapterID: "codex", Launcher: controlagents.LauncherChoiceGlobal,
	})
	if err != nil {
		t.Fatalf("resolveACPConnectionLauncher() error = %v", err)
	}
	if gotSpec != "@agentclientprotocol/codex-acp@1.1.7" || connection.Launcher.Command != installedBin {
		t.Fatalf("installed connection = %#v, spec=%q", connection, gotSpec)
	}
}

func TestResolveACPConnectionLauncherUsesRegistryNPXMetadata(t *testing.T) {
	binDir := t.TempDir()
	npx := writeExternalAgentExecutable(t, binDir, "npx")
	t.Setenv("PATH", binDir)

	connection, err := (&controlCommandBackend{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
		AdapterID: "factory-droid", Launcher: controlagents.LauncherChoiceNPX,
	})
	if err != nil {
		t.Fatalf("resolveACPConnectionLauncher() error = %v", err)
	}
	if connection.ID != "factory-droid" || connection.Launcher.Command != npx || connection.Launcher.Kind != controlagents.LaunchKindPackageExec {
		t.Fatalf("connection = %#v, want Registry npx launcher", connection)
	}
	wantArgs := []string{"-y", "droid@0.181.0", "exec", "--output-format", "acp-daemon"}
	if !reflect.DeepEqual(connection.Launcher.Args, wantArgs) {
		t.Fatalf("launcher args = %#v, want %#v", connection.Launcher.Args, wantArgs)
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

	connection, err := (&controlCommandBackend{}).resolveACPConnectionLauncher(context.Background(), controlagents.ConnectRequest{
		AdapterID: "claude", Launcher: controlagents.LauncherChoiceGlobal,
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") || connection.Launcher.Command != "" {
		t.Fatalf("resolveACPConnectionLauncher() = %#v, %v", connection, err)
	}
}

func TestManagedACPAdapterPackageMatchChecksPackageAndVersion(t *testing.T) {
	root := t.TempDir()
	pkg := builtinACPAdapterPackage{Package: "@agentclientprotocol/codex-acp", Version: "1.1.2", Bin: "codex-acp"}
	dir := filepath.Join(root, "node_modules", "@agentclientprotocol", "codex-acp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@agentclientprotocol/codex-acp","version":"1.1.2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !managedACPAdapterPackageMatches(root, pkg) {
		t.Fatal("managedACPAdapterPackageMatches() = false for curated package")
	}
	pkg.Version = "1.1.3"
	if managedACPAdapterPackageMatches(root, pkg) {
		t.Fatal("managedACPAdapterPackageMatches() = true for stale version")
	}
}

func TestSplitACPCommandLinePreservesQuotedExecutableAndArguments(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "agent bins")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	wantCommand := writeExternalAgentExecutable(t, binDir, "custom acp")
	command, args, err := splitACPCommandLine(`"` + wantCommand + `" --mode "deep review"`)
	if err != nil {
		t.Fatal(err)
	}
	if command != wantCommand || len(args) != 2 || args[0] != "--mode" || args[1] != "deep review" {
		t.Fatalf("split command = %q %#v", command, args)
	}
}

func TestRosterNameValidationRejectsRuntimeAndRunAddresses(t *testing.T) {
	for _, name := range []string{"self", "local", "main", "kernel", "sandbox", "guardian", "reviewer", "status", "worker(lina)", "bad name"} {
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
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("test executable fixture\n"), 0o700); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("Abs(%s) error = %v", path, err)
	}
	return abs
}
