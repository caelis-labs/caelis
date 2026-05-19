//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/runnerruntime"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setup"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setupstate"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestRuntimeDescribeReportsWindowsElevatedCapabilities(t *testing.T) {
	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	desc := rt.Describe()
	if desc.Backend != sandbox.BackendWindowsElevated {
		t.Fatalf("Backend = %q, want %q", desc.Backend, sandbox.BackendWindowsElevated)
	}
	if !desc.Capabilities.CommandExec || !desc.Capabilities.AsyncSessions || !desc.Capabilities.PathPolicy {
		t.Fatalf("Capabilities = %+v, want command exec, async sessions, path policy", desc.Capabilities)
	}
	status := rt.Status()
	if status.ResolvedBackend != sandbox.BackendWindowsElevated {
		t.Fatalf("Status = %+v", status)
	}
}

func TestRuntimeDoesNotAutoElevateWhenSetupIsMissing(t *testing.T) {
	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()

	result, err := rt.Run(context.Background(), sandbox.CommandRequest{
		Command: "Write-Output should-not-run",
		Dir:     t.TempDir(),
		Timeout: time.Second,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindowsElevated,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkDisabled,
		},
	})
	if err == nil {
		t.Fatalf("Run() error = nil; result=%+v", result)
	}
	if strings.Contains(err.Error(), "/sandbox setup") || !strings.Contains(err.Error(), "global setup is required") {
		t.Fatalf("Run() error = %v, want model-safe global setup error without slash command guidance", err)
	}
}

func TestFullSetupPayloadCarriesWorkspacePolicy(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: stateDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	payload, _, err := windowsRT.runner.setupPayload(runnerruntimeRequest(workspace), setup.SetupKindFull)
	if err != nil {
		t.Fatalf("setupPayload(full) error = %v", err)
	}
	if !containsPath(payload.Policy.WriteRoots, workspace) {
		t.Fatalf("full setup write roots = %#v, want workspace %q", payload.Policy.WriteRoots, workspace)
	}
	if payload.WorkspacePolicyHash == "" {
		t.Fatal("WorkspacePolicyHash is empty")
	}
	if payload.GlobalPolicyHash == "" {
		t.Fatal("GlobalPolicyHash is empty")
	}
	if _, err := os.Stat(filepath.Join(stateDir, ".sandbox-bin")); !os.IsNotExist(err) {
		t.Fatalf("full setup status materialized helper bin dir or unexpected error: %v", err)
	}
}

func TestSetupFreshnessIgnoresRunnerAndPolicyHashChanges(t *testing.T) {
	stateDir := t.TempDir()
	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	payload, err := windowsRT.runner.globalSetupPayload()
	if err != nil {
		t.Fatalf("setupPayload(full) error = %v", err)
	}
	dirs := setupstate.NewDirs(stateDir)
	if err := setupstate.WriteMarker(dirs.MarkerPath, setupstate.Marker{
		Version:         setup.PayloadVersion,
		RunnerHash:      "old-runner-hash",
		PolicyHash:      "old-policy-hash",
		OfflineUsername: payload.OfflineUsername,
		OnlineUsername:  payload.OnlineUsername,
		OwnerUsername:   payload.OwnerUsername,
	}); err != nil {
		t.Fatalf("WriteMarker() error = %v", err)
	}
	if freshness := windowsRT.runner.freshnessForPayload(payload); !freshness.Current {
		t.Fatalf("freshness = %+v, want current despite hash changes", freshness)
	}
}

func TestSetupReadyFreshnessDetectsStaleSandboxCredentials(t *testing.T) {
	stateDir := t.TempDir()
	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	payload, err := windowsRT.runner.globalSetupPayload()
	if err != nil {
		t.Fatalf("setupPayload(full) error = %v", err)
	}
	dirs := setupstate.NewDirs(stateDir)
	if err := setupstate.WriteMarker(dirs.MarkerPath, setupstate.Marker{
		Version:         setup.PayloadVersion,
		OfflineUsername: payload.OfflineUsername,
		OnlineUsername:  payload.OnlineUsername,
		OwnerUsername:   payload.OwnerUsername,
	}); err != nil {
		t.Fatalf("WriteMarker() error = %v", err)
	}
	bogus, err := win32.ProtectMachineString("definitely-not-the-current-password", "caelis test stale sandbox password")
	if err != nil {
		t.Fatalf("ProtectMachineString() error = %v", err)
	}
	data, err := json.Marshal(setup.UsersFile{
		Offline: setup.UserSecret{Username: payload.OfflineUsername, PasswordProtected: bogus},
		Online:  setup.UserSecret{Username: payload.OnlineUsername, PasswordProtected: bogus},
	})
	if err != nil {
		t.Fatalf("Marshal users file error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dirs.UsersPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(users dir) error = %v", err)
	}
	if err := os.WriteFile(dirs.UsersPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile(users) error = %v", err)
	}
	freshness := windowsRT.runner.setupReadyFreshness()
	if freshness.Current || !strings.Contains(freshness.Reason, "credentials are stale") {
		t.Fatalf("setupReadyFreshness() = %+v, want stale credentials", freshness)
	}
}

func TestStatusSeparatesGlobalAndWorkspaceSetup(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: stateDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	payload, err := windowsRT.runner.globalSetupPayload()
	if err != nil {
		t.Fatalf("globalSetupPayload() error = %v", err)
	}
	dirs := setupstate.NewDirs(stateDir)
	if err := setupstate.WriteMarker(dirs.MarkerPath, setupstate.Marker{
		Version:         setup.PayloadVersion,
		OfflineUsername: payload.OfflineUsername,
		OnlineUsername:  payload.OnlineUsername,
		OwnerUsername:   payload.OwnerUsername,
	}); err != nil {
		t.Fatalf("WriteMarker() error = %v", err)
	}
	if err := setupstate.WriteWorkspace(dirs.WorkspacePath, setupstate.WorkspaceRecord{
		Version:       1,
		WorkspaceRoot: workspace,
		PolicyHash:    "stale-policy",
	}); err != nil {
		t.Fatalf("WriteWorkspace() error = %v", err)
	}
	windowsRT.runner.usersReadyMu.Lock()
	windowsRT.runner.usersReadyCheckedAt = time.Now()
	windowsRT.runner.usersReadyErr = ""
	windowsRT.runner.usersReadyMu.Unlock()

	status := rt.Status()
	if !status.GlobalSetupCurrent || status.GlobalSetupRequired {
		t.Fatalf("global setup status = current %t required %t reason %q", status.GlobalSetupCurrent, status.GlobalSetupRequired, status.GlobalSetupReason)
	}
	if status.SetupMarkerReason != "" {
		t.Fatalf("SetupMarkerReason = %q, want empty global marker reason", status.SetupMarkerReason)
	}
	if !status.WorkspaceSetupRequired || status.WorkspaceSetupCurrent {
		t.Fatalf("workspace setup status = current %t required %t reason %q", status.WorkspaceSetupCurrent, status.WorkspaceSetupRequired, status.WorkspaceSetupReason)
	}
	if !strings.Contains(strings.ToLower(status.WorkspaceSetupReason), "capability") {
		t.Fatalf("WorkspaceSetupReason = %q, want missing capability SID reason", status.WorkspaceSetupReason)
	}
	if !status.SetupRequired {
		t.Fatal("SetupRequired = false, want aggregate required")
	}
}

func TestSetupUsernamesAreStateScoped(t *testing.T) {
	first := setupOfflineUser(`C:\Users\Administrator\.caelis`)
	second := setupOfflineUser(`C:\Users\Administrator\AppData\Local\Temp\caelis-e2e`)
	if first == second {
		t.Fatalf("state-scoped usernames are equal: %q", first)
	}
	if len(first) > 20 || len(setupOnlineUser(`C:\Users\Administrator\.caelis`)) > 20 {
		t.Fatalf("state-scoped usernames exceed Windows local user length: offline=%q online=%q", first, setupOnlineUser(`C:\Users\Administrator\.caelis`))
	}
}

func TestSiblingRunnerHelperPrefersDedicatedRunner(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "caelis.exe")
	runner := filepath.Join(dir, "caelis-command-runner.exe")
	if err := os.WriteFile(main, []byte("main"), 0o600); err != nil {
		t.Fatalf("WriteFile(main) error = %v", err)
	}
	if got := siblingRunnerHelper(main); got != "" {
		t.Fatalf("siblingRunnerHelper without runner = %q, want empty", got)
	}
	if err := os.WriteFile(runner, []byte("runner"), 0o600); err != nil {
		t.Fatalf("WriteFile(runner) error = %v", err)
	}
	if got := siblingRunnerHelper(main); !strings.EqualFold(got, runner) {
		t.Fatalf("siblingRunnerHelper = %q, want %q", got, runner)
	}
}

func TestSiblingSetupHelperPrefersDedicatedSetup(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "caelis.exe")
	setup := filepath.Join(dir, "caelis-windows-sandbox-setup.exe")
	if err := os.WriteFile(main, []byte("main"), 0o600); err != nil {
		t.Fatalf("WriteFile(main) error = %v", err)
	}
	if got := siblingSetupHelper(main); got != "" {
		t.Fatalf("siblingSetupHelper without setup helper = %q, want empty", got)
	}
	if err := os.WriteFile(setup, []byte("setup"), 0o600); err != nil {
		t.Fatalf("WriteFile(setup) error = %v", err)
	}
	if got := siblingSetupHelper(main); !strings.EqualFold(got, setup) {
		t.Fatalf("siblingSetupHelper = %q, want %q", got, setup)
	}
}

func TestRefreshPolicyCacheTracksPolicyHash(t *testing.T) {
	r := &setupRunner{}
	if r.refreshAlreadyApplied("abc") {
		t.Fatal("refreshAlreadyApplied before mark = true, want false")
	}
	r.markRefreshApplied("abc")
	if !r.refreshAlreadyApplied("abc") {
		t.Fatal("refreshAlreadyApplied after mark = false, want true")
	}
	if r.refreshAlreadyApplied("def") {
		t.Fatal("refreshAlreadyApplied for different hash = true, want false")
	}
	r.clearRefreshCache()
	if r.refreshAlreadyApplied("abc") {
		t.Fatal("refreshAlreadyApplied after clear = true, want false")
	}
}

func runnerruntimeRequest(dir string) runnerruntime.Request {
	return runnerruntime.Request{
		Dir: dir,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindowsElevated,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkDisabled,
		},
	}
}

func containsPath(paths []string, want string) bool {
	wantInfo, wantErr := os.Stat(want)
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && wantErr == nil && os.SameFile(info, wantInfo) {
			return true
		}
		if strings.EqualFold(filepath.Clean(path), filepath.Clean(want)) {
			return true
		}
	}
	return false
}
