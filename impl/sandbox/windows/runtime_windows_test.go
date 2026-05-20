//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/runnerruntime"
	winpolicy "github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/policy"
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

func TestFullSetupPayloadSkipsGlobalDeveloperCachePolicy(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	cacheRoot := filepath.Join(t.TempDir(), "go-cache")
	t.Setenv("GOCACHE", cacheRoot)
	t.Setenv("GOMODCACHE", filepath.Join(t.TempDir(), "go-mod-cache"))
	t.Setenv("npm_config_cache", filepath.Join(t.TempDir(), "npm-cache"))
	t.Setenv("TEMP", filepath.Join(t.TempDir(), "temp"))
	t.Setenv("TMP", filepath.Join(t.TempDir(), "tmp"))
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
	for _, root := range []string{cacheRoot, os.Getenv("GOMODCACHE"), os.Getenv("npm_config_cache"), os.Getenv("TEMP"), os.Getenv("TMP")} {
		if containsPath(payload.GlobalPolicy.WriteRoots, root) {
			t.Fatalf("global setup write roots = %#v, did not expect host developer cache/temp root %q", payload.GlobalPolicy.WriteRoots, root)
		}
	}
	if len(payload.GlobalPolicy.WriteRoots) != 0 || len(payload.GlobalPolicy.CapabilitySIDs) != 0 || len(payload.GlobalPolicy.DenyReadPaths) != 0 || len(payload.GlobalPolicy.DenyWritePaths) != 0 {
		t.Fatalf("global setup policy = %+v, want no host write/cache/secret ACL targets", payload.GlobalPolicy)
	}
	if containsPath(payload.Policy.WriteRoots, cacheRoot) {
		t.Fatalf("workspace policy write roots = %#v, did not expect global cache root", payload.Policy.WriteRoots)
	}
}

func TestRuntimeRefreshDeltaPolicySkipsGlobalRoots(t *testing.T) {
	globalRoot := filepath.Join(t.TempDir(), "cache")
	workspace := filepath.Join(t.TempDir(), "workspace")
	hidden := filepath.Join(workspace, "hidden")
	protected := filepath.Join(globalRoot, "secret")
	child := filepath.Join(globalRoot, "go-build")

	got := runtimeRefreshDeltaPolicy(winpolicy.Policy{
		ReadRoots:                 []string{child, workspace},
		WriteRoots:                []string{globalRoot, child, workspace},
		DenyReadPaths:             []string{protected, hidden},
		DenyWritePaths:            []string{protected, hidden},
		MaterializeDenyWritePaths: []string{protected, hidden},
		CapabilitySIDs:            []string{"S-1-5-21-1-2-3-4", "S-1-5-21-5-6-7-8"},
		WriteRootCapabilitySIDs: map[string]string{
			globalRoot: "S-1-5-21-1-2-3-4",
			child:      "S-1-5-21-1-2-3-4",
			workspace:  "S-1-5-21-5-6-7-8",
		},
	}, winpolicy.Policy{
		ReadRoots:      []string{globalRoot},
		WriteRoots:     []string{globalRoot},
		DenyReadPaths:  []string{protected},
		DenyWritePaths: []string{protected},
	})

	if containsPath(got.ReadRoots, child) || containsPath(got.WriteRoots, child) || containsPath(got.WriteRoots, globalRoot) {
		t.Fatalf("delta roots = read %#v write %#v, want global roots removed", got.ReadRoots, got.WriteRoots)
	}
	if !containsPath(got.WriteRoots, workspace) || !containsPath(got.DenyWritePaths, hidden) {
		t.Fatalf("delta roots = write %#v deny %#v, want workspace and hidden path retained", got.WriteRoots, got.DenyWritePaths)
	}
	if _, ok := got.WriteRootCapabilitySIDs[workspace]; !ok {
		t.Fatalf("WriteRootCapabilitySIDs = %#v, want workspace capability retained", got.WriteRootCapabilitySIDs)
	}
	if _, ok := got.WriteRootCapabilitySIDs[globalRoot]; ok {
		t.Fatalf("WriteRootCapabilitySIDs = %#v, want global capability removed", got.WriteRootCapabilitySIDs)
	}
}

func TestSetupFreshnessIgnoresRunnerHashButDetectsPolicyHashChanges(t *testing.T) {
	stateDir := t.TempDir()
	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	if _, err := windowsRT.runner.globalSetupPolicy(true); err != nil {
		t.Fatalf("globalSetupPolicy(bind) error = %v", err)
	}
	payload, err := windowsRT.runner.globalSetupPayload()
	if err != nil {
		t.Fatalf("setupPayload(full) error = %v", err)
	}
	dirs := setupstate.NewDirs(stateDir)
	if err := setupstate.WriteMarker(dirs.MarkerPath, setupstate.Marker{
		Version:         setup.PayloadVersion,
		RunnerHash:      "old-runner-hash",
		PolicyHash:      payload.GlobalPolicyHash,
		OfflineUsername: payload.OfflineUsername,
		OnlineUsername:  payload.OnlineUsername,
		OwnerUsername:   payload.OwnerUsername,
	}); err != nil {
		t.Fatalf("WriteMarker() error = %v", err)
	}
	if freshness := windowsRT.runner.freshnessForPayload(payload); !freshness.Current {
		t.Fatalf("freshness = %+v, want current despite runner hash change", freshness)
	}
	marker, err := setupstate.ReadMarker(dirs.MarkerPath)
	if err != nil {
		t.Fatalf("ReadMarker() error = %v", err)
	}
	marker.PolicyHash = "different-policy-hash"
	if err := setupstate.WriteMarker(dirs.MarkerPath, marker); err != nil {
		t.Fatalf("WriteMarker(stale policy) error = %v", err)
	}
	if freshness := windowsRT.runner.freshnessForPayload(payload); freshness.Current || !strings.Contains(freshness.Reason, "policy hash changed") {
		t.Fatalf("freshness = %+v, want policy hash changed", freshness)
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
	if _, err := windowsRT.runner.globalSetupPolicy(true); err != nil {
		t.Fatalf("globalSetupPolicy(bind) error = %v", err)
	}
	payload, err := windowsRT.runner.globalSetupPayload()
	if err != nil {
		t.Fatalf("setupPayload(full) error = %v", err)
	}
	dirs := setupstate.NewDirs(stateDir)
	if err := setupstate.WriteMarker(dirs.MarkerPath, setupstate.Marker{
		Version:         setup.PayloadVersion,
		PolicyHash:      payload.GlobalPolicyHash,
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
	if _, err := windowsRT.runner.globalSetupPolicy(true); err != nil {
		t.Fatalf("globalSetupPolicy(bind) error = %v", err)
	}
	payload, err := windowsRT.runner.globalSetupPayload()
	if err != nil {
		t.Fatalf("globalSetupPayload() error = %v", err)
	}
	dirs := setupstate.NewDirs(stateDir)
	if err := setupstate.WriteMarker(dirs.MarkerPath, setupstate.Marker{
		Version:         setup.PayloadVersion,
		PolicyHash:      payload.GlobalPolicyHash,
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
	global, ok := status.Setup.Check("global")
	if !ok {
		t.Fatalf("missing global setup check in %+v", status.Setup)
	}
	if !global.Current || global.Required {
		t.Fatalf("global setup status = current %t required %t reason %q", global.Current, global.Required, global.Reason)
	}
	if global.Reason != "" {
		t.Fatalf("global setup reason = %q, want empty global marker reason", global.Reason)
	}
	workspaceCheck, ok := status.Setup.Check("workspace")
	if !ok {
		t.Fatalf("missing workspace setup check in %+v", status.Setup)
	}
	if !workspaceCheck.Required || workspaceCheck.Current {
		t.Fatalf("workspace setup status = current %t required %t reason %q", workspaceCheck.Current, workspaceCheck.Required, workspaceCheck.Reason)
	}
	if !strings.Contains(strings.ToLower(workspaceCheck.Reason), "capability") {
		t.Fatalf("WorkspaceSetupReason = %q, want missing capability SID reason", workspaceCheck.Reason)
	}
	if !status.Setup.Required {
		t.Fatal("Setup.Required = false, want aggregate required")
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

func TestCommitHelperTempReusesConcurrentTarget(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, ".caelis-helper.tmp")
	target := filepath.Join(dir, "caelis-command-runner-test.exe")
	data := []byte("runner-helper")
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile(tmp) error = %v", err)
	}
	hash, err := fileHash(tmpPath)
	if err != nil {
		t.Fatalf("fileHash(tmp) error = %v", err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	got, err := commitHelperTemp(tmpPath, target, hash)
	if err != nil {
		t.Fatalf("commitHelperTemp() error = %v", err)
	}
	if !strings.EqualFold(got, target) {
		t.Fatalf("commitHelperTemp() = %q, want %q", got, target)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("temp file still exists or unexpected stat error: %v", err)
	}
	if !helperFileHashMatches(target, hash) {
		t.Fatal("target helper hash changed")
	}
}

func TestMaterializeHelperFromSourceConcurrentReusesHashedTarget(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.exe")
	if err := os.WriteFile(source, []byte("runner-helper"), 0o600); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	r := &setupRunner{stateRoot: t.TempDir()}
	const workers = 8
	paths := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, _, err := r.materializeHelperFromSource(source, "caelis-command-runner-")
			if err != nil {
				errs <- err
				return
			}
			paths <- path
		}()
	}
	wg.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		t.Fatalf("materializeHelperFromSource() error = %v", err)
	}
	var first string
	for path := range paths {
		if first == "" {
			first = path
			continue
		}
		if !strings.EqualFold(path, first) {
			t.Fatalf("materialized path = %q, want %q", path, first)
		}
	}
	if first == "" {
		t.Fatal("no materialized helper path returned")
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("materialized helper stat error = %v", err)
	}
}

func TestRefreshPolicyCacheTracksPolicyAliases(t *testing.T) {
	r := &setupRunner{}
	if r.refreshAlreadyApplied("abc") {
		t.Fatal("refreshAlreadyApplied before mark = true, want false")
	}
	if r.refreshAnyApplied("abc", "def") {
		t.Fatal("refreshAnyApplied before mark = true, want false")
	}
	r.markRefreshApplied("abc", "def")
	if !r.refreshAlreadyApplied("abc") {
		t.Fatal("refreshAlreadyApplied after mark = false, want true")
	}
	if !r.refreshAlreadyApplied("def") {
		t.Fatal("refreshAlreadyApplied for alias = false, want true")
	}
	if !r.refreshAnyApplied("missing", "def") {
		t.Fatal("refreshAnyApplied for alias = false, want true")
	}
	keys := refreshCacheKeys("abc", "", "def", "abc")
	if len(keys) != 2 || keys[0] != "abc" || keys[1] != "def" {
		t.Fatalf("refreshCacheKeys() = %#v, want abc/def", keys)
	}
	r.clearRefreshCache()
	if r.refreshAlreadyApplied("abc") {
		t.Fatal("refreshAlreadyApplied after clear = true, want false")
	}
}

func TestMarkRefreshAppliedForRequestTracksRequestAndWorkspacePolicy(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	r := &setupRunner{
		cfg:        sandbox.Config{CWD: workspace},
		stateRoot:  stateDir,
		executable: os.Args[0],
	}
	req := r.baseSetupRequest()
	requestKey, err := r.policyRequestKey(req)
	if err != nil {
		t.Fatalf("policyRequestKey() error = %v", err)
	}
	payload, _, err := r.setupPayload(req, setup.SetupKindRuntimeRefresh)
	if err != nil {
		t.Fatalf("setupPayload(runtime_refresh) error = %v", err)
	}
	if r.refreshAnyApplied(requestKey, payload.WorkspacePolicyHash) {
		t.Fatal("refresh cache populated before mark")
	}
	r.markRefreshAppliedForRequest(req)
	if !r.refreshAlreadyApplied(requestKey) {
		t.Fatal("request key was not marked refreshed")
	}
	if !r.refreshAlreadyApplied(payload.WorkspacePolicyHash) {
		t.Fatal("workspace policy hash was not marked refreshed")
	}
}

func TestBaseRefreshCacheCoversDefaultCommandWorkspacePolicy(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	r := &setupRunner{
		cfg:        sandbox.Config{CWD: workspace},
		stateRoot:  stateDir,
		executable: os.Args[0],
	}
	commandReq := runnerruntime.Request{
		Dir: workspace,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindowsElevated,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkDisabled,
			PathRules: []sandbox.PathRule{
				{Path: workspace, Access: sandbox.PathAccessReadWrite},
			},
		},
	}
	payload, _, err := r.setupPayload(commandReq, setup.SetupKindRuntimeRefresh)
	if err != nil {
		t.Fatalf("setupPayload(runtime_refresh) error = %v", err)
	}
	requestKey, err := r.policyRequestKey(commandReq)
	if err != nil {
		t.Fatalf("policyRequestKey() error = %v", err)
	}
	r.markBaseRefreshApplied()
	if !r.refreshAnyApplied(requestKey, payload.WorkspacePolicyHash) {
		t.Fatalf("refresh cache did not cover default command workspace policy; workspace hash %q", payload.WorkspacePolicyHash)
	}
}

func TestContextMutexLockHonorsContextCancellation(t *testing.T) {
	var mu contextMutex
	if err := mu.Lock(context.Background()); err != nil {
		t.Fatalf("initial Lock() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mu.Lock(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Lock() error = %v, want context.Canceled", err)
	}
	mu.Unlock()
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
