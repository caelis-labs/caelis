//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/acl"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/pathutil"
	"github.com/OnslaughtSnail/caelis/internal/testenv"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestRuntimeDescribeReportsRestrictedTokenCapabilities(t *testing.T) {
	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()

	desc := rt.Describe()
	if desc.Backend != sandbox.BackendWindows {
		t.Fatalf("Backend = %q, want %q", desc.Backend, sandbox.BackendWindows)
	}
	if desc.Isolation != sandbox.IsolationProcess {
		t.Fatalf("Isolation = %q, want process", desc.Isolation)
	}
	if !desc.Capabilities.CommandExec || !desc.Capabilities.AsyncSessions || !desc.Capabilities.FileSystem {
		t.Fatalf("Capabilities = %+v, want filesystem, command exec, async", desc.Capabilities)
	}
	if desc.Capabilities.NetworkControl {
		t.Fatalf("NetworkControl = true, want false")
	}
	if desc.Capabilities.TTY {
		t.Fatalf("TTY = true, want false until ConPTY is supported on restricted token path")
	}
}

func TestEffectiveWindowsSandboxNetworkFallsBackOnline(t *testing.T) {
	t.Parallel()

	for _, network := range []sandbox.Network{
		"",
		sandbox.NetworkInherit,
		sandbox.NetworkEnabled,
		sandbox.NetworkDisabled,
	} {
		if got := effectiveWindowsSandboxNetwork(network); got != sandbox.NetworkEnabled {
			t.Fatalf("effectiveWindowsSandboxNetwork(%q) = %q, want enabled", network, got)
		}
	}
}

func TestWindowsSessionForceTerminateMarksDone(t *testing.T) {
	t.Parallel()

	waitErr := errors.New("forced termination")
	session := &windowsSession{
		ref: sandbox.SessionRef{
			Backend:   sandbox.BackendWindows,
			SessionID: "exec-test",
		},
		terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendWindows,
			SessionID:  "exec-test",
			TerminalID: "term-test",
		},
		running:   true,
		exitCode:  0,
		startedAt: time.Now(),
		updatedAt: time.Now(),
		done:      make(chan struct{}),
	}

	session.forceTerminated(waitErr)
	status, err := session.Wait(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Running {
		t.Fatalf("status.Running = true, want false")
	}
	if status.ExitCode != -1 {
		t.Fatalf("status.ExitCode = %d, want -1", status.ExitCode)
	}
	result, err := session.Result(context.Background())
	if !errors.Is(err, waitErr) {
		t.Fatalf("Result() error = %v, want forced termination", err)
	}
	if result.ExitCode != -1 {
		t.Fatalf("result.ExitCode = %d, want -1", result.ExitCode)
	}

	session.forceTerminated(errors.New("second force should be ignored"))
	result, err = session.Result(context.Background())
	if !errors.Is(err, waitErr) {
		t.Fatalf("second Result() error = %v, want first forced termination", err)
	}
	if result.ExitCode != -1 {
		t.Fatalf("second result.ExitCode = %d, want -1", result.ExitCode)
	}
}

func TestStatusIsCheapAndDoesNotCreateSIDStore(t *testing.T) {
	stateDir := t.TempDir()
	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()

	status := rt.Status()
	if status.ResolvedBackend != sandbox.BackendWindows {
		t.Fatalf("ResolvedBackend = %q, want windows", status.ResolvedBackend)
	}
	if status.Setup.Required {
		t.Fatalf("Setup.Required = true, want lazy optional setup")
	}
	if _, err := os.Stat(filepath.Join(stateDir, ".sandbox", "cap_sids.json")); !os.IsNotExist(err) {
		t.Fatalf("Status created cap_sids.json or unexpected stat error: %v", err)
	}
}

func TestStatusReportsLastWorkspaceSetupError(t *testing.T) {
	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	windowsRT.recordWorkspaceSetupError(errors.New("acl: write D:\\xue\\code\\cmpctl DACL: Access is denied."))
	status := rt.Status()
	if !status.Setup.Required {
		t.Fatalf("Setup.Required = false, want explicit repair required")
	}
	check, ok := status.Setup.Check("workspace")
	if !ok {
		t.Fatalf("Setup checks = %#v, want workspace check", status.Setup.Checks)
	}
	if !check.Required || check.Current {
		t.Fatalf("workspace check = %+v, want required and not current", check)
	}
	for _, want := range []string{"acl: write", "Access is denied", "caelis sandbox fix"} {
		if !strings.Contains(status.Setup.Error+check.Error+check.Details["manual_fix_hint"], want) {
			t.Fatalf("workspace setup status = %+v, want %q", status.Setup, want)
		}
	}

	windowsRT.clearWorkspaceSetupError()
	status = rt.Status()
	if status.Setup.Required {
		t.Fatalf("Setup.Required after clear = true, want false")
	}
}

func TestRunElevatedRepairUsesInternalHelperRequest(t *testing.T) {
	workspace := t.TempDir()
	state := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: state})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	oldLauncher := launchElevatedRepairProcess
	defer func() { launchElevatedRepairProcess = oldLauncher }()
	var gotExe string
	var gotCWD string
	var gotArgs []string
	launchElevatedRepairProcess = func(_ context.Context, exe string, args []string, cwd string) (uint32, error) {
		gotExe = exe
		gotCWD = cwd
		gotArgs = append([]string(nil), args...)
		configFile := flagValue(args, "-config-file")
		resultFile := flagValue(args, "-result-file")
		if configFile == "" || resultFile == "" {
			t.Fatalf("repair helper args = %#v, want config and result files", args)
		}
		data, err := os.ReadFile(configFile)
		if err != nil {
			t.Fatalf("read repair config: %v", err)
		}
		var request elevatedRepairRequest
		if err := json.Unmarshal(data, &request); err != nil {
			t.Fatalf("decode repair config: %v", err)
		}
		if request.Version != elevatedRepairRequestVersion {
			t.Fatalf("repair request version = %d, want %d", request.Version, elevatedRepairRequestVersion)
		}
		if pathutil.Key(request.Config.CWD) != pathutil.Key(workspace) {
			t.Fatalf("repair request cwd = %q, want %q", request.Config.CWD, workspace)
		}
		if request.Config.RequestedBackend != sandbox.BackendWindows {
			t.Fatalf("repair request backend = %q, want windows", request.Config.RequestedBackend)
		}
		if err := writeElevatedRepairResult(resultFile, nil); err != nil {
			t.Fatalf("write repair result: %v", err)
		}
		return 0, nil
	}

	if err := windowsRT.runElevatedRepair(context.Background()); err != nil {
		t.Fatalf("runElevatedRepair() error = %v", err)
	}
	if gotExe == "" {
		t.Fatalf("launcher executable was empty")
	}
	if pathutil.Key(gotCWD) != pathutil.Key(workspace) {
		t.Fatalf("launcher cwd = %q, want %q", gotCWD, workspace)
	}
	if len(gotArgs) == 0 || gotArgs[0] != internalRepairHelperCommand {
		t.Fatalf("launcher args = %#v, want internal helper command", gotArgs)
	}
}

func TestValidateElevatedRepairConfigAllowsPolicyWritableRoots(t *testing.T) {
	workspace := t.TempDir()
	state := t.TempDir()
	existingOutsideWorkspace := filepath.Join(t.TempDir(), "global-skills")
	if err := os.MkdirAll(existingOutsideWorkspace, 0o700); err != nil {
		t.Fatalf("MkdirAll(existingOutsideWorkspace) error = %v", err)
	}
	missingOutsideWorkspace := filepath.Join(t.TempDir(), "missing-global-skills")
	missingInsideWorkspace := filepath.Join(workspace, ".agents", "skills")

	err := validateElevatedRepairConfig(sandbox.Config{
		CWD:              workspace,
		StateDir:         state,
		RequestedBackend: sandbox.BackendWindows,
		WritableRoots: []string{
			existingOutsideWorkspace,
			missingOutsideWorkspace,
			missingInsideWorkspace,
		},
	})
	if err != nil {
		t.Fatalf("validateElevatedRepairConfig() error = %v", err)
	}
}

func flagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestPolicyForRequestUsesOnlyWritableRootsAndDenyWriteCarveouts(t *testing.T) {
	workspace := t.TempDir()
	commandDir := filepath.Join(workspace, "subdir")
	extraWrite := filepath.Join(t.TempDir(), "extra-write")
	extraRead := filepath.Join(t.TempDir(), "extra-read")
	hidden := filepath.Join(workspace, "secret")
	outDir := filepath.Join(workspace, "out")
	for _, dir := range []string{commandDir, extraWrite, extraRead, hidden, outDir, filepath.Join(workspace, ".git"), filepath.Join(workspace, "vendor")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	rt, err := New(sandbox.Config{
		CWD:              workspace,
		StateDir:         t.TempDir(),
		WritableRoots:    []string{extraWrite},
		ReadableRoots:    []string{extraRead},
		ReadOnlySubpaths: []string{"vendor"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	policy, err := windowsRT.policyForRequest(sandbox.CommandRequest{
		Dir: commandDir,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkDisabled,
			PathRules: []sandbox.PathRule{
				{Path: extraRead, Access: sandbox.PathAccessReadOnly},
				{Path: hidden, Access: sandbox.PathAccessHidden},
				{Path: filepath.Join(workspace, "out"), Access: sandbox.PathAccessReadWrite},
			},
		},
	})
	if err != nil {
		t.Fatalf("policyForRequest() error = %v", err)
	}
	for _, want := range []string{workspace, commandDir, extraWrite, outDir} {
		if !containsPath(policy.WriteRoots, want) {
			t.Fatalf("WriteRoots = %#v, want %q", policy.WriteRoots, want)
		}
	}
	for _, unexpected := range []string{extraRead, hidden} {
		if containsPath(policy.WriteRoots, unexpected) || containsPath(policy.DenyWritePaths, unexpected) {
			t.Fatalf("policy unexpectedly consumed read/hidden path %q: %+v", unexpected, policy)
		}
	}
	for _, want := range []string{filepath.Join(workspace, ".git"), filepath.Join(workspace, "vendor")} {
		if !containsPath(policy.DenyWritePaths, want) {
			t.Fatalf("DenyWritePaths = %#v, want %q", policy.DenyWritePaths, want)
		}
	}
	if len(policy.CapabilitySIDs) == 0 {
		t.Fatalf("CapabilitySIDs empty, want active write SID set")
	}
}

func TestPolicyRejectsUnsupportedPermissionMode(t *testing.T) {
	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	_, err = windowsRT.policyForRequest(sandbox.CommandRequest{
		Constraints: sandbox.Constraints{Permission: sandbox.PermissionFullAccess},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("policyForRequest(full access) error = %v, want unsupported", err)
	}
}

func TestEnsureWritesManifestAndIsIdempotent(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()
	rt, err := New(sandbox.Config{
		CWD:              workspace,
		StateDir:         stateDir,
		ReadOnlySubpaths: []string{"readonly"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	req := sandbox.CommandRequest{
		Dir: workspace,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
		},
	}
	first, err := windowsRT.ensureForRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ensureForRequest(first) error = %v", err)
	}
	manifestPath := windowsRT.manifestPath()
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("manifest stat error = %v", err)
	}
	if first.SandboxEnvRoot == "" {
		t.Fatalf("SandboxEnvRoot empty, want state-backed sandbox environment root")
	}
	if !containsPath(first.WriteRoots, first.SandboxEnvRoot) {
		t.Fatalf("WriteRoots = %#v, want sandbox env root %q", first.WriteRoots, first.SandboxEnvRoot)
	}
	if pathutil.IsUnder(first.SandboxEnvRoot, workspace) {
		t.Fatalf("SandboxEnvRoot = %q, want outside workspace %q", first.SandboxEnvRoot, workspace)
	}
	envSID := first.sidForWriteRoot(first.SandboxEnvRoot)
	if envSID == "" {
		t.Fatalf("sandbox env SID empty for %q", first.SandboxEnvRoot)
	}
	for _, dir := range sandboxEnvDirs(first.SandboxEnvRoot) {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("sandbox env dir %q stat error = %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("sandbox env path %q is not a directory", dir)
		}
		if missing, err := acl.MissingFileDACLEntries(dir, allowEntries(envSID)...); err != nil || len(missing) != 0 {
			t.Fatalf("sandbox env dir %q missing ACL entries = %#v/%v, want repaired", dir, missing, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, ".caelis-sandbox")); !os.IsNotExist(err) {
		t.Fatalf("workspace sandbox env stat error = %v, want not created", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "readonly")); !os.IsNotExist(err) {
		t.Fatalf("workspace readonly stat error = %v, want not auto-created", err)
	}
	second, err := windowsRT.ensureForRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ensureForRequest(second) error = %v", err)
	}
	info2, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatalf("manifest second stat error = %v", err)
	}
	if first.PolicyHash != second.PolicyHash || !sameStringSet(first.CapabilitySIDs, second.CapabilitySIDs) {
		t.Fatalf("ensure policies differ: first=%+v second=%+v", first, second)
	}
	if info2.ModTime().Before(info.ModTime()) {
		t.Fatalf("manifest mtime moved backwards: %s -> %s", info.ModTime(), info2.ModTime())
	}
}

func TestSandboxEnvironmentKeepsHomeSandboxedAndExposesHostSkillRoot(t *testing.T) {
	hostHome := t.TempDir()
	testenv.SetHome(t, hostHome)
	envRoot := filepath.Join(t.TempDir(), "env")

	env, err := sandboxEnvironment(workspacePolicy{SandboxEnvRoot: envRoot}, nil)
	if err != nil {
		t.Fatalf("sandboxEnvironment() error = %v", err)
	}
	sandboxHome := filepath.Join(envRoot, "home")
	if got := envValue(env, "HOME"); got != sandboxHome {
		t.Fatalf("HOME = %q, want sandbox home %q", got, sandboxHome)
	}
	if got := envValue(env, "USERPROFILE"); got != sandboxHome {
		t.Fatalf("USERPROFILE = %q, want sandbox home %q", got, sandboxHome)
	}
	if got := envValue(env, "CAELIS_SKILLS_DIR"); got != filepath.Join(hostHome, ".caelis", "skills") {
		t.Fatalf("CAELIS_SKILLS_DIR = %q, want host skill root", got)
	}
}

func TestEnsureSkipsMissingWritableRootsAndRepairsWhenPresent(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()
	missingRoot := filepath.Join(workspace, ".agents", "skills")
	rt, err := New(sandbox.Config{
		CWD:              workspace,
		StateDir:         stateDir,
		WritableRoots:    []string{missingRoot},
		ReadOnlySubpaths: []string{"readonly"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	if _, err := windowsRT.ensureForRequest(context.Background(), sandbox.CommandRequest{Dir: workspace}); err != nil {
		t.Fatalf("ensureForRequest() error = %v", err)
	}
	policyBefore, err := windowsRT.policyForRequest(sandbox.CommandRequest{Dir: workspace})
	if err != nil {
		t.Fatalf("policyForRequest(before create) error = %v", err)
	}
	if containsPath(policyBefore.WriteRoots, missingRoot) {
		t.Fatalf("WriteRoots = %#v, did not expect missing root %q", policyBefore.WriteRoots, missingRoot)
	}
	if _, err := os.Stat(missingRoot); !os.IsNotExist(err) {
		t.Fatalf("missing writable root stat error = %v, want not created", err)
	}
	if _, err := os.Stat(filepath.Join(missingRoot, "readonly")); !os.IsNotExist(err) {
		t.Fatalf("missing writable root readonly stat error = %v, want not auto-created", err)
	}
	if err := os.MkdirAll(missingRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(missingRoot) error = %v", err)
	}
	if _, err := windowsRT.ensureForRequest(context.Background(), sandbox.CommandRequest{Dir: workspace}); err != nil {
		t.Fatalf("ensureForRequest(after create) error = %v", err)
	}
	policy, err := windowsRT.policyForRequest(sandbox.CommandRequest{Dir: workspace})
	if err != nil {
		t.Fatalf("policyForRequest() error = %v", err)
	}
	if !containsPath(policy.WriteRoots, missingRoot) {
		t.Fatalf("WriteRoots = %#v, want existing root %q", policy.WriteRoots, missingRoot)
	}
	if missing, err := acl.MissingFileDACLEntries(missingRoot, allowEntries(policy.sidForWriteRoot(missingRoot))...); err != nil || len(missing) != 0 {
		t.Fatalf("missing writable root ACL entries = %#v/%v, want repaired", missing, err)
	}
}

func TestRepairCurrentWorkspaceACLsCleansStaleManifestACLs(t *testing.T) {
	workspace := t.TempDir()
	staleRoot := filepath.Join(t.TempDir(), "stale-write")
	if err := os.MkdirAll(staleRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(staleRoot) error = %v", err)
	}
	rt, err := New(sandbox.Config{
		CWD:           workspace,
		StateDir:      t.TempDir(),
		WritableRoots: []string{staleRoot},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	oldPolicy, err := windowsRT.ensureForRequest(context.Background(), sandbox.CommandRequest{Dir: workspace})
	if err != nil {
		t.Fatalf("ensureForRequest() error = %v", err)
	}
	staleEntries := allowEntries(oldPolicy.sidForWriteRoot(staleRoot))
	if len(staleEntries) == 0 {
		t.Fatalf("old policy missing stale root SID: %+v", oldPolicy)
	}
	if missing, err := acl.MissingFileDACLEntries(staleRoot, staleEntries...); err != nil || len(missing) != 0 {
		t.Fatalf("stale root ACL entries before repair = %#v/%v, want present", missing, err)
	}

	windowsRT.cfg.WritableRoots = nil
	if err := windowsRT.repairCurrentWorkspaceACLs(context.Background()); err != nil {
		t.Fatalf("repairCurrentWorkspaceACLs() error = %v", err)
	}
	missing, err := acl.MissingFileDACLEntries(staleRoot, staleEntries...)
	if err != nil {
		t.Fatalf("MissingFileDACLEntries(after repair) error = %v", err)
	}
	if len(missing) == 0 {
		t.Fatalf("stale root ACL entries remained after repair")
	}
	manifest, err := windowsRT.readManifest()
	if err != nil {
		t.Fatalf("readManifest() error = %v", err)
	}
	if containsPath(manifest.WriteRoots, staleRoot) {
		t.Fatalf("manifest WriteRoots = %#v, did not expect stale root %q", manifest.WriteRoots, staleRoot)
	}
}

func TestCappedOutputBufferDecodesPowerShellCLIXML(t *testing.T) {
	raw := "#< CLIXML\r\n" +
		`<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04">` +
		`<Obj S="progress" RefId="0"><MS><PR N="Record"><AV>Preparing modules for first use.</AV></PR></MS></Obj>` +
		`<S S="Error">Property Length cannot be found._x000D__x000A_</S>` +
		`</Objs>`
	buf := &cappedOutputBuffer{max: windowsOutputCap}
	if _, err := buf.Write([]byte(raw)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "Property Length cannot be found.\r\n") {
		t.Fatalf("String() = %q, want decoded PowerShell error", got)
	}
	if strings.Contains(got, "<Objs") || strings.Contains(got, "Preparing modules") {
		t.Fatalf("String() = %q, want XML/progress stripped", got)
	}
}

func TestCleanupPlanIncludesNewManifestAndLegacyArtifacts(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: stateDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	if _, err := windowsRT.ensureForRequest(context.Background(), sandbox.CommandRequest{Dir: windowsRT.cfg.CWD}); err != nil {
		t.Fatalf("ensureForRequest() error = %v", err)
	}

	plan := windowsRT.cleanupPlan()
	if !containsPath(plan.LegacyPaths, windowsRT.manifestPath()) {
		t.Fatalf("cleanup LegacyPaths = %#v, want new manifest", plan.LegacyPaths)
	}
	if !containsPath(plan.LegacyPaths, filepath.Join(stateDir, ".sandbox-bin")) ||
		!containsPath(plan.LegacyPaths, filepath.Join(stateDir, ".sandbox-secrets")) {
		t.Fatalf("cleanup LegacyPaths = %#v, want legacy helper/secrets dirs", plan.LegacyPaths)
	}
	if !containsPath(plan.LegacyPaths, filepath.Join(workspace, ".caelis-sandbox")) {
		t.Fatalf("cleanup LegacyPaths = %#v, want workspace sandbox env dir", plan.LegacyPaths)
	}
	if !containsPath(plan.LegacyPaths, windowsRT.sandboxEnvBase()) {
		t.Fatalf("cleanup LegacyPaths = %#v, want state sandbox env base", plan.LegacyPaths)
	}
	if len(plan.ACLPaths) == 0 || len(plan.Principals) == 0 {
		t.Fatalf("cleanup plan = %+v, want ACL paths and principals", plan)
	}
	if len(plan.LegacyProtected) == 0 {
		t.Fatalf("cleanup plan = %+v, want protected legacy artifact reports", plan)
	}
}

func TestSandboxedCommandSmoke(t *testing.T) {
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_SMOKE_E2E") != "1" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_SMOKE_E2E=1 to run the Windows workspace-write sandbox smoke test")
	}
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{
		CWD:              workspace,
		StateDir:         t.TempDir(),
		WritableRoots:    []string{filepath.Join(workspace, ".agents", "skills")},
		ReadOnlySubpaths: []string{"readonly"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := rt.Run(ctx, sandbox.CommandRequest{
		Command: "Set-Content -LiteralPath .\\ok.txt -Value ok; Get-Content -LiteralPath .\\ok.txt",
		Dir:     workspace,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	})
	if err != nil {
		t.Fatalf("workspace write command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "ok") {
		t.Fatalf("stdout = %q, want ok", result.Stdout)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".caelis-sandbox")); !os.IsNotExist(err) {
		t.Fatalf("workspace sandbox env stat error = %v, want not created", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "readonly")); !os.IsNotExist(err) {
		t.Fatalf("workspace readonly stat error = %v, want not auto-created", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".agents", "skills")); !os.IsNotExist(err) {
		t.Fatalf("missing workspace skill root stat error = %v, want not auto-created", err)
	}

	result, err = rt.Run(ctx, sandbox.CommandRequest{
		Command: "Write-Progress -Activity preparing -Status modules; Write-Error 'length cannot be found'; exit 1",
		Dir:     workspace,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	})
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("PowerShell error command unexpectedly succeeded: result=%+v", result)
	}
	if !strings.Contains(result.Stderr, "length cannot be found") {
		t.Fatalf("stderr = %q, want decoded PowerShell error", result.Stderr)
	}
	if strings.Contains(result.Stderr, "#< CLIXML") || strings.Contains(result.Stderr, "<Objs") || strings.Contains(result.Stderr, "Preparing modules") {
		t.Fatalf("stderr = %q, want CLIXML/progress stripped", result.Stderr)
	}

	result, err = rt.Run(ctx, sandbox.CommandRequest{
		Command: "& where.exe cmd.exe",
		Dir:     workspace,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	})
	if err != nil || result.ExitCode != 0 || !strings.Contains(strings.ToLower(result.Stdout), "cmd.exe") {
		t.Fatalf("where.exe smoke err=%v result=%+v", err, result)
	}

	result, err = rt.Run(ctx, sandbox.CommandRequest{
		Command: "$ErrorActionPreference='Stop'; $dir = Join-Path $env:TEMP 'pip-unpack-smoke'; New-Item -ItemType Directory -Force -Path $dir | Out-Null; Set-Content -LiteralPath (Join-Path $dir 'ok.txt') -Value ok; Get-Content -LiteralPath (Join-Path $dir 'ok.txt')",
		Dir:     workspace,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	})
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, "ok") {
		t.Fatalf("sandbox TEMP write smoke err=%v result=%+v", err, result)
	}

	if python, ok := availablePythonForSiteCustomize(); ok {
		pythonCommand := python.shellPrefix()
		result, err = rt.Run(ctx, sandbox.CommandRequest{
			Command: pythonCommand + ` -c "import tempfile,pathlib; d=tempfile.mkdtemp(prefix='pip-unpack-'); p=pathlib.Path(d)/'ok.txt'; p.write_text('ok', encoding='utf-8'); print(p.read_text(encoding='utf-8'))"`,
			Dir:     workspace,
			Constraints: sandbox.Constraints{
				Route:      sandbox.RouteSandbox,
				Backend:    sandbox.BackendWindows,
				Permission: sandbox.PermissionWorkspaceWrite,
				Network:    sandbox.NetworkEnabled,
			},
		})
		if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, "ok") {
			t.Fatalf("python tempfile private dir write smoke err=%v result=%+v", err, result)
		}

		result, err = rt.Run(ctx, sandbox.CommandRequest{
			Command: pythonCommand + ` -c "print('requests 2.34.2'); print('HTTP 200')" 2>&1`,
			Dir:     workspace,
			Constraints: sandbox.Constraints{
				Route:      sandbox.RouteSandbox,
				Backend:    sandbox.BackendWindows,
				Permission: sandbox.PermissionWorkspaceWrite,
				Network:    sandbox.NetworkEnabled,
			},
		})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("python stdout newline smoke err=%v result=%+v", err, result)
		}
		if got := strings.ReplaceAll(result.Stdout, "\r\n", "\n"); got != "requests 2.34.2\nHTTP 200\n" {
			t.Fatalf("python stdout newline smoke stdout = %q, want line breaks preserved", result.Stdout)
		}

		var streamed strings.Builder
		session, err := rt.Start(ctx, sandbox.CommandRequest{
			Command: pythonCommand + ` -c "print('requests 2.34.2'); print('HTTP 200')" 2>&1`,
			Dir:     workspace,
			OnOutput: func(chunk sandbox.OutputChunk) {
				if chunk.Stream == "stdout" {
					streamed.WriteString(chunk.Text)
				}
			},
			Constraints: sandbox.Constraints{
				Route:      sandbox.RouteSandbox,
				Backend:    sandbox.BackendWindows,
				Permission: sandbox.PermissionWorkspaceWrite,
				Network:    sandbox.NetworkEnabled,
			},
		})
		if err != nil {
			t.Fatalf("python stdout streaming start error = %v", err)
		}
		status, err := session.Wait(ctx, 30*time.Second)
		if err != nil || status.Running {
			t.Fatalf("python stdout streaming wait err=%v status=%+v", err, status)
		}
		result, err = session.Result(ctx)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("python stdout streaming result err=%v result=%+v", err, result)
		}
		if got := strings.ReplaceAll(result.Stdout, "\r\n", "\n"); got != "requests 2.34.2\nHTTP 200\n" {
			t.Fatalf("python stdout streaming result stdout = %q, want line breaks preserved", result.Stdout)
		}
		if got := strings.ReplaceAll(streamed.String(), "\r\n", "\n"); got != "requests 2.34.2\nHTTP 200\n" {
			t.Fatalf("python stdout streaming chunks = %q, want line breaks preserved", streamed.String())
		}
	}

	if _, gitErr := exec.LookPath("git"); gitErr == nil {
		result, err = rt.Run(ctx, sandbox.CommandRequest{
			Command: "$env:GIT_TRACE='1'; git ls-remote https://127.0.0.1:1/caelis.git HEAD",
			Dir:     workspace,
			Constraints: sandbox.Constraints{
				Route:      sandbox.RouteSandbox,
				Backend:    sandbox.BackendWindows,
				Permission: sandbox.PermissionWorkspaceWrite,
				Network:    sandbox.NetworkEnabled,
			},
		})
		merged := result.Stdout + "\n" + result.Stderr
		if strings.Contains(merged, "cannot create standard input pipe") || strings.Contains(merged, "unable to fork") {
			t.Fatalf("git helper pipe/fork failed err=%v result=%+v", err, result)
		}
	}

	tempTarget := filepath.Join(os.TempDir(), "caelis-windows-sandbox-denied.txt")
	_ = os.Remove(tempTarget)
	escaped := strings.ReplaceAll(tempTarget, "'", "''")
	result, err = rt.Run(ctx, sandbox.CommandRequest{
		Command: "$ErrorActionPreference='Stop'; Set-Content -LiteralPath '" + escaped + "' -Value denied",
		Dir:     workspace,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
		},
	})
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("temp write unexpectedly succeeded: result=%+v", result)
	}

	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		homeTarget := filepath.Join(home, "caelis-windows-sandbox-denied.txt")
		_ = os.Remove(homeTarget)
		escapedHome := strings.ReplaceAll(homeTarget, "'", "''")
		result, err = rt.Run(ctx, sandbox.CommandRequest{
			Command: "$ErrorActionPreference='Stop'; Set-Content -LiteralPath '" + escapedHome + "' -Value denied",
			Dir:     workspace,
			Constraints: sandbox.Constraints{
				Route:      sandbox.RouteSandbox,
				Backend:    sandbox.BackendWindows,
				Permission: sandbox.PermissionWorkspaceWrite,
			},
		})
		if err == nil || result.ExitCode == 0 {
			_ = os.Remove(homeTarget)
			t.Fatalf("home write unexpectedly succeeded: result=%+v", result)
		}
	}
}

func containsPath(paths []string, want string) bool {
	wantKey := pathutil.Key(want)
	for _, path := range paths {
		if pathutil.Key(path) == wantKey {
			return true
		}
	}
	return false
}

func envValue(env []string, name string) string {
	prefix := strings.ToUpper(name) + "="
	for _, item := range env {
		if strings.HasPrefix(strings.ToUpper(item), prefix) {
			return item[len(prefix):]
		}
	}
	return ""
}
