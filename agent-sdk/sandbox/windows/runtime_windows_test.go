//go:build windows

package windows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/acl"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
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

func TestWindowsSessionTerminalObservationIncludesDecoderTail(t *testing.T) {
	t.Parallel()

	tailStarted := make(chan struct{})
	releaseTail := make(chan struct{})
	session := &windowsSession{
		ref: sandbox.SessionRef{
			Backend:   sandbox.BackendWindows,
			SessionID: "exec-tail",
		},
		terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendWindows,
			SessionID:  "exec-tail",
			TerminalID: "term-tail",
		},
		running:      true,
		startedAt:    time.Now(),
		updatedAt:    time.Now(),
		done:         make(chan struct{}),
		outputSignal: make(chan struct{}),
		onOutput: func(chunk sandbox.OutputChunk) {
			if chunk.Stream == "stdout" {
				close(tailStarted)
				<-releaseTail
			}
		},
	}
	if got := session.stdoutText.Decode([]byte{0xe4, 0xb8}); len(got) != 0 {
		t.Fatalf("decoder emitted incomplete UTF-8 prefix %q before Flush", got)
	}

	forceDone := make(chan struct{})
	go func() {
		session.forceTerminated(errors.New("forced after partial output"))
		close(forceDone)
	}()
	<-tailStarted

	observation := make(chan sandbox.OutputObservation, 1)
	go func() {
		got, _ := session.AwaitOutput(context.Background(), sandbox.OutputCursor{})
		observation <- got
	}()
	select {
	case got := <-observation:
		t.Fatalf("terminal observation published before tail callback completed: %+v", got)
	case <-time.After(30 * time.Millisecond):
	}

	close(releaseTail)
	select {
	case <-forceDone:
	case <-time.After(time.Second):
		t.Fatal("forceTerminated did not wait for final publication")
	}
	got := <-observation
	if got.Status.Running || got.Cursor.Stdout == 0 {
		t.Fatalf("terminal observation = %+v, want decoder tail cursor", got)
	}
	stdout, _, cursor, _, err := session.ReadOutput(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("ReadOutput() error = %v", err)
	}
	if cursor != got.Cursor.Stdout || len(stdout) == 0 {
		t.Fatalf("ReadOutput() = %q/%d, observation cursor = %d", stdout, cursor, got.Cursor.Stdout)
	}
}

func TestWindowsSessionConcurrentFinalizersWaitForTerminalPublish(t *testing.T) {
	t.Parallel()

	tailStarted := make(chan struct{})
	releaseTail := make(chan struct{})
	normalErr := errors.New("normal finalizer")
	session := &windowsSession{
		running:      true,
		done:         make(chan struct{}),
		outputSignal: make(chan struct{}),
		onOutput: func(chunk sandbox.OutputChunk) {
			if chunk.Stream == "stdout" {
				close(tailStarted)
				<-releaseTail
			}
		},
	}
	if got := session.stdoutText.Decode([]byte{0xe4, 0xb8}); len(got) != 0 {
		t.Fatalf("decoder emitted incomplete UTF-8 prefix %q before Flush", got)
	}

	go session.finalize(normalErr, false)
	<-tailStarted
	forceDone := make(chan struct{})
	go func() {
		session.forceTerminated(errors.New("concurrent force"))
		close(forceDone)
	}()
	select {
	case <-forceDone:
		t.Fatal("losing finalizer returned before terminal publication")
	case <-time.After(30 * time.Millisecond):
	}

	close(releaseTail)
	select {
	case <-forceDone:
	case <-time.After(time.Second):
		t.Fatal("losing finalizer did not observe completed terminal publication")
	}
	result, err := session.Result(context.Background())
	if !errors.Is(err, normalErr) || result.ExitCode != 0 {
		t.Fatalf("Result() = %+v/%v, want first finalizer result", result, err)
	}
}

func TestWindowsSessionTailCallbackCanTerminateWithoutDeadlock(t *testing.T) {
	t.Parallel()

	callbackDone := make(chan error, 1)
	session := &windowsSession{
		running:      true,
		done:         make(chan struct{}),
		outputSignal: make(chan struct{}),
	}
	session.onOutput = func(chunk sandbox.OutputChunk) {
		if chunk.Stream == "stdout" {
			callbackDone <- session.Terminate(context.Background())
		}
	}
	if got := session.stdoutText.Decode([]byte{0xe4, 0xb8}); len(got) != 0 {
		t.Fatalf("decoder emitted incomplete UTF-8 prefix %q before Flush", got)
	}

	go session.finalize(nil, false)
	select {
	case err := <-callbackDone:
		if err != nil {
			t.Fatalf("Terminate() from tail callback error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Terminate() from tail callback deadlocked")
	}
	select {
	case <-session.done:
	case <-time.After(time.Second):
		t.Fatal("terminal publication did not complete after tail callback")
	}
}

func TestWindowsSessionOutputCallbackCanTerminateWithoutDeadlock(t *testing.T) {
	t.Parallel()

	callbackDone := make(chan error, 1)
	session := &windowsSession{
		running:      true,
		cancel:       func() {},
		done:         make(chan struct{}),
		outputSignal: make(chan struct{}),
	}
	session.onOutput = func(sandbox.OutputChunk) {
		callbackDone <- session.Terminate(context.Background())
	}

	go session.emitOutput(sandbox.OutputChunk{Stream: "stdout", Text: "stop"})
	select {
	case err := <-callbackDone:
		if err != nil {
			t.Fatalf("Terminate() from output callback error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Terminate() from output callback deadlocked")
	}
	session.finalize(nil, false)
}

func TestWindowsSessionAwaitOutputDoesNotRegressBlockedSiblingCursor(t *testing.T) {
	t.Parallel()

	stdoutCallback := make(chan struct{})
	releaseStdout := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseStdout) })
	session := &windowsSession{
		running:      true,
		outputSignal: make(chan struct{}),
		onOutput: func(chunk sandbox.OutputChunk) {
			if chunk.Stream == "stdout" && chunk.Text != "" {
				close(stdoutCallback)
				<-releaseStdout
			}
		},
	}
	session.wg.Add(2)
	go session.readStream(bytes.NewReader([]byte("x")), "stdout")
	<-stdoutCallback
	go session.readStream(bytes.NewReader([]byte("e")), "stderr")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	observation, err := session.AwaitOutput(ctx, sandbox.OutputCursor{Stdout: 1})
	if err != nil {
		t.Fatalf("AwaitOutput() error = %v", err)
	}
	if observation.Cursor != (sandbox.OutputCursor{Stdout: 1, Stderr: 1}) {
		t.Fatalf("AwaitOutput().Cursor = %+v, want monotonic stdout 1/stderr 1", observation.Cursor)
	}

	releaseOnce.Do(func() { close(releaseStdout) })
	session.wg.Wait()
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
	hidden := filepath.Join(workspace, "secret")
	outDir := filepath.Join(workspace, "out")
	for _, dir := range []string{commandDir, extraWrite, hidden, outDir, filepath.Join(workspace, ".git"), filepath.Join(workspace, "vendor")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	rt, err := New(sandbox.Config{
		CWD:              workspace,
		StateDir:         t.TempDir(),
		WritableRoots:    []string{extraWrite},
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
	if containsPath(policy.WriteRoots, hidden) || containsPath(policy.DenyWritePaths, hidden) {
		t.Fatalf("policy unexpectedly consumed hidden path %q: %+v", hidden, policy)
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

func TestFileSystemForIgnoresWindowsHiddenPathRules(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	hidden := filepath.Join(workspace, "secret")
	gitDir := filepath.Join(workspace, ".git")
	for _, dir := range []string{outside, hidden, gitDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	outsideFile := filepath.Join(outside, "note.txt")
	hiddenFile := filepath.Join(hidden, "token.txt")
	for _, path := range []string{outsideFile, hiddenFile} {
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	rt, err := New(sandbox.Config{
		CWD:      workspace,
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()

	fsys := rt.FileSystemFor(sandbox.Constraints{
		Permission: sandbox.PermissionWorkspaceWrite,
		PathRules:  []sandbox.PathRule{{Path: hidden, Access: sandbox.PathAccessHidden}},
	})
	for _, path := range []string{outsideFile, hiddenFile} {
		if _, err := fsys.ReadFile(path); err != nil {
			t.Fatalf("ReadFile(%s) error = %v, want Windows current-user readable path allowed", path, err)
		}
	}
	if err := fsys.WriteFile(filepath.Join(hidden, "new.txt"), []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile(hidden workspace path) error = %v, want hidden rule ignored on Windows", err)
	}
	if err := fsys.WriteFile(filepath.Join(gitDir, "index.lock"), []byte("data"), 0o600); err == nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("WriteFile(.git) error = %v, want deny-write carveout permission denied", err)
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
	if _, err := os.Stat(filepath.Join(first.SandboxEnvRoot, "home")); !os.IsNotExist(err) {
		t.Fatalf("sandbox fake home stat error = %v, want not created", err)
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

func TestSandboxEnvironmentPreservesHostUserDirsAndRedirectsToolCaches(t *testing.T) {
	envRoot := filepath.Join(t.TempDir(), "sandbox-env")
	hostHome := filepath.Join(t.TempDir(), "host-home")
	hostAppData := filepath.Join(hostHome, "AppData", "Roaming")
	hostLocalAppData := filepath.Join(hostHome, "AppData", "Local")
	hostDrive := filepath.VolumeName(hostHome)
	hostPath := strings.TrimPrefix(strings.TrimPrefix(hostHome, hostDrive), string(filepath.Separator))
	if hostPath != "" {
		hostPath = string(filepath.Separator) + hostPath
	}
	hostPythonPath := filepath.Join(t.TempDir(), "host-python")
	extraPythonPath := filepath.Join(t.TempDir(), "extra-python")
	setHomeForWindowsTest(t, hostHome)
	t.Setenv("APPDATA", hostAppData)
	t.Setenv("LOCALAPPDATA", hostLocalAppData)
	t.Setenv("PYTHONPATH", hostPythonPath)
	unsetEnvForTest(t, "NUGET_PACKAGES", "pnpm_config_store_dir", "npm_config_store_dir", "YARN_CACHE_FOLDER")

	env, err := sandboxEnvironment(workspacePolicy{SandboxEnvRoot: envRoot}, map[string]string{
		"PYTHONPATH": extraPythonPath,
	})
	if err != nil {
		t.Fatalf("sandboxEnvironment() error = %v", err)
	}

	envRoot = pathutil.Normalize(envRoot)
	tempRoot := sandboxTempRoot(envRoot)
	cacheRoot := sandboxCacheRoot(envRoot)
	for key, want := range map[string]string{
		"HOME":                      hostHome,
		"USERPROFILE":               hostHome,
		"APPDATA":                   hostAppData,
		"LOCALAPPDATA":              hostLocalAppData,
		"HOMEDRIVE":                 hostDrive,
		"HOMEPATH":                  hostPath,
		"CAELIS_SKILLS_DIR":         filepath.Join(hostHome, ".caelis", "skills"),
		"TEMP":                      tempRoot,
		"TMP":                       tempRoot,
		"GOTMPDIR":                  tempRoot,
		"CAELIS_SANDBOX_TEMP":       tempRoot,
		"GOCACHE":                   filepath.Join(cacheRoot, "go-build"),
		"GOMODCACHE":                filepath.Join(cacheRoot, "go-mod"),
		"GOTELEMETRY":               "off",
		"PIP_CACHE_DIR":             filepath.Join(cacheRoot, "pip"),
		"npm_config_cache":          filepath.Join(cacheRoot, "npm"),
		"NUGET_PACKAGES":            filepath.Join(cacheRoot, "nuget", "packages"),
		"pnpm_config_store_dir":     filepath.Join(cacheRoot, "pnpm-store"),
		"npm_config_store_dir":      filepath.Join(cacheRoot, "pnpm-store"),
		"YARN_CACHE_FOLDER":         filepath.Join(cacheRoot, "yarn"),
		"PSModuleAnalysisCachePath": filepath.Join(sandboxPowerShellCacheDir(envRoot), "PowerShell_AnalysisCache"),
		"PYTHONPATH":                prependEnvPath(sandboxPythonSiteDir(envRoot), extraPythonPath),
	} {
		if got, ok := envValue(env, key); !ok || got != want {
			t.Fatalf("env[%s] = %q/%v, want %q", key, got, ok, want)
		}
	}
	if got, ok := envValue(env, "CAELIS_SANDBOX_HOME"); ok {
		t.Fatalf("env[CAELIS_SANDBOX_HOME] = %q, want absent", got)
	}
	if _, err := os.Stat(filepath.Join(envRoot, "home")); !os.IsNotExist(err) {
		t.Fatalf("sandbox fake home stat error = %v, want not created", err)
	}
	for _, dir := range []string{
		tempRoot,
		filepath.Join(cacheRoot, "go-build"),
		filepath.Join(cacheRoot, "go-mod"),
		filepath.Join(cacheRoot, "npm"),
		filepath.Join(cacheRoot, "pip"),
		filepath.Join(cacheRoot, "pnpm-store"),
		filepath.Join(cacheRoot, "nuget", "packages"),
		filepath.Join(cacheRoot, "yarn"),
		sandboxPythonSiteDir(envRoot),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("sandbox cache dir %q stat error = %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("sandbox cache path %q is not a directory", dir)
		}
	}
}

func TestSandboxEnvironmentUsesNativeOpenSSHForGitWhenAvailable(t *testing.T) {
	unsetEnvForTest(t, "GIT_SSH_COMMAND", "GIT_SSH")
	envRoot := filepath.Join(t.TempDir(), "sandbox-env")
	_, sshPath := withFakeSystemOpenSSH(t)

	env, err := sandboxEnvironment(workspacePolicy{SandboxEnvRoot: envRoot}, nil)
	if err != nil {
		t.Fatalf("sandboxEnvironment() error = %v", err)
	}

	got, ok := envValue(env, "GIT_SSH_COMMAND")
	if !ok || got != filepath.ToSlash(sshPath) {
		t.Fatalf("env[GIT_SSH_COMMAND] = %q/%v, want %q", got, ok, filepath.ToSlash(sshPath))
	}
}

func TestSandboxEnvironmentDoesNotOverrideGitSSHSelection(t *testing.T) {
	unsetEnvForTest(t, "GIT_SSH_COMMAND", "GIT_SSH")
	envRoot := filepath.Join(t.TempDir(), "sandbox-env")
	withFakeSystemOpenSSH(t)

	env, err := sandboxEnvironment(workspacePolicy{SandboxEnvRoot: envRoot}, map[string]string{
		"GIT_SSH_COMMAND": "C:/custom/ssh.exe -F C:/custom/config",
	})
	if err != nil {
		t.Fatalf("sandboxEnvironment(command override) error = %v", err)
	}
	if got, ok := envValue(env, "GIT_SSH_COMMAND"); !ok || got != "C:/custom/ssh.exe -F C:/custom/config" {
		t.Fatalf("env[GIT_SSH_COMMAND] = %q/%v, want command override", got, ok)
	}

	env, err = sandboxEnvironment(workspacePolicy{SandboxEnvRoot: envRoot}, map[string]string{
		"GIT_SSH": "C:/custom/ssh.exe",
	})
	if err != nil {
		t.Fatalf("sandboxEnvironment(path override) error = %v", err)
	}
	if got, ok := envValue(env, "GIT_SSH"); !ok || got != "C:/custom/ssh.exe" {
		t.Fatalf("env[GIT_SSH] = %q/%v, want path override", got, ok)
	}
	if got, ok := envValue(env, "GIT_SSH_COMMAND"); ok {
		t.Fatalf("env[GIT_SSH_COMMAND] = %q, want absent when GIT_SSH is explicit", got)
	}
}

func TestSandboxEnvironmentSkipsGitOpenSSHWhenUnavailable(t *testing.T) {
	unsetEnvForTest(t, "GIT_SSH_COMMAND", "GIT_SSH")
	envRoot := filepath.Join(t.TempDir(), "sandbox-env")
	t.Setenv("SystemRoot", filepath.Join(t.TempDir(), "Windows"))

	env, err := sandboxEnvironment(workspacePolicy{SandboxEnvRoot: envRoot}, nil)
	if err != nil {
		t.Fatalf("sandboxEnvironment() error = %v", err)
	}
	if got, ok := envValue(env, "GIT_SSH_COMMAND"); ok {
		t.Fatalf("env[GIT_SSH_COMMAND] = %q, want absent without system OpenSSH", got)
	}
}

func TestSandboxEnvironmentPreservesToolCacheOverrides(t *testing.T) {
	unsetEnvForTest(t,
		"GOCACHE", "GOMODCACHE", "PIP_CACHE_DIR", "npm_config_cache",
		"NUGET_PACKAGES", "pnpm_config_store_dir", "npm_config_store_dir", "YARN_CACHE_FOLDER",
	)
	envRoot := filepath.Join(t.TempDir(), "sandbox-env")
	extra := map[string]string{
		"GOCACHE":               filepath.Join(t.TempDir(), "go-build"),
		"GOMODCACHE":            filepath.Join(t.TempDir(), "go-mod"),
		"PIP_CACHE_DIR":         filepath.Join(t.TempDir(), "pip"),
		"npm_config_cache":      filepath.Join(t.TempDir(), "npm"),
		"NUGET_PACKAGES":        filepath.Join(t.TempDir(), "nuget"),
		"pnpm_config_store_dir": filepath.Join(t.TempDir(), "pnpm"),
		"YARN_CACHE_FOLDER":     filepath.Join(t.TempDir(), "yarn"),
	}

	env, err := sandboxEnvironment(workspacePolicy{SandboxEnvRoot: envRoot}, extra)
	if err != nil {
		t.Fatalf("sandboxEnvironment() error = %v", err)
	}
	for key, want := range extra {
		if got, ok := envValue(env, key); !ok || got != want {
			t.Fatalf("env[%s] = %q/%v, want %q", key, got, ok, want)
		}
	}
	if got, ok := envValue(env, "npm_config_store_dir"); ok {
		t.Fatalf("env[npm_config_store_dir] = %q, want absent when pnpm_config_store_dir is explicit", got)
	}
}

func TestSandboxEnvironmentPreservesHostDefaultCacheOverridesAndRedirectsForcedCaches(t *testing.T) {
	unsetEnvForTest(t,
		"GOCACHE", "GOMODCACHE", "PIP_CACHE_DIR", "npm_config_cache",
		"NUGET_PACKAGES", "pnpm_config_store_dir", "npm_config_store_dir", "YARN_CACHE_FOLDER",
	)
	envRoot := filepath.Join(t.TempDir(), "sandbox-env")
	hostGOCache := filepath.Join(t.TempDir(), "host-go-build")
	hostGoModCache := filepath.Join(t.TempDir(), "host-go-mod")
	hostPip := filepath.Join(t.TempDir(), "host-pip")
	hostNPM := filepath.Join(t.TempDir(), "host-npm")
	hostNuGet := filepath.Join(t.TempDir(), "host-nuget")
	hostPnpm := filepath.Join(t.TempDir(), "host-pnpm")
	hostYarn := filepath.Join(t.TempDir(), "host-yarn")
	t.Setenv("GOCACHE", hostGOCache)
	t.Setenv("GOMODCACHE", hostGoModCache)
	t.Setenv("PIP_CACHE_DIR", hostPip)
	t.Setenv("npm_config_cache", hostNPM)
	t.Setenv("NUGET_PACKAGES", hostNuGet)
	t.Setenv("npm_config_store_dir", hostPnpm)
	t.Setenv("YARN_CACHE_FOLDER", hostYarn)

	env, err := sandboxEnvironment(workspacePolicy{SandboxEnvRoot: envRoot}, nil)
	if err != nil {
		t.Fatalf("sandboxEnvironment() error = %v", err)
	}
	cacheRoot := sandboxCacheRoot(pathutil.Normalize(envRoot))
	for key, want := range map[string]string{
		"GOCACHE":          filepath.Join(cacheRoot, "go-build"),
		"GOMODCACHE":       filepath.Join(cacheRoot, "go-mod"),
		"PIP_CACHE_DIR":    filepath.Join(cacheRoot, "pip"),
		"npm_config_cache": filepath.Join(cacheRoot, "npm"),
	} {
		if got, ok := envValue(env, key); !ok || got != want {
			t.Fatalf("env[%s] = %q/%v, want sandbox cache %q despite host value", key, got, ok, want)
		}
	}
	for key, want := range map[string]string{
		"NUGET_PACKAGES":       hostNuGet,
		"npm_config_store_dir": hostPnpm,
		"YARN_CACHE_FOLDER":    hostYarn,
	} {
		if got, ok := envValue(env, key); !ok || got != want {
			t.Fatalf("env[%s] = %q/%v, want host override %q", key, got, ok, want)
		}
	}
	if got, ok := envValue(env, "pnpm_config_store_dir"); ok {
		t.Fatalf("env[pnpm_config_store_dir] = %q, want absent when npm_config_store_dir is explicit", got)
	}
}

func TestCleanupSandboxCachesPreservesActiveEnvRoot(t *testing.T) {
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	active := windowsRT.sandboxEnvRoot(workspace)
	old := filepath.Join(windowsRT.sandboxEnvBase(), "old-workspace")
	for _, dir := range []string{filepath.Join(active, "cache"), filepath.Join(old, "cache")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cache.bin"), []byte("cache"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", dir, err)
		}
	}
	stale := time.Now().Add(-(windowsCacheMaxAge + time.Hour))
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatalf("Chtimes(old) error = %v", err)
	}
	if err := os.Chtimes(active, stale, stale); err != nil {
		t.Fatalf("Chtimes(active) error = %v", err)
	}

	if err := windowsRT.cleanupSandboxCaches(context.Background(), active); err != nil {
		t.Fatalf("cleanupSandboxCaches() error = %v", err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("active env stat error = %v, want preserved", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old env stat error = %v, want removed", err)
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

	oldPolicy, err := windowsRT.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
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

func TestUnsafeWritableRootReasonRejectsBroadUserRoots(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Dir(home)
	project := filepath.Join(home, "project")

	for _, root := range []string{home, parent} {
		if reason := unsafeWritableRootReason(root, home); reason == "" {
			t.Fatalf("unsafeWritableRootReason(%q, %q) = empty, want rejection", root, home)
		}
	}
	if volume := filepath.VolumeName(home); volume != "" {
		if reason := unsafeWritableRootReason(volume+string(filepath.Separator), home); reason == "" {
			t.Fatalf("unsafeWritableRootReason(volume root, %q) = empty, want rejection", home)
		}
	}
	if reason := unsafeWritableRootReason(project, home); reason != "" {
		t.Fatalf("unsafeWritableRootReason(%q, %q) = %q, want allowed", project, home, reason)
	}
}

func TestEnsureForRequestReturnsCoreWritableRootACLFailure(t *testing.T) {
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	oldModify := modifyFileDACL
	defer func() { modifyFileDACL = oldModify }()
	modifyFileDACL = func(path string, entries ...acl.Entry) error {
		if pathutil.Key(path) == pathutil.Key(workspace) {
			return syscall.ERROR_ACCESS_DENIED
		}
		return oldModify(path, entries...)
	}

	_, err = windowsRT.ensureForRequest(context.Background(), sandbox.CommandRequest{Dir: workspace})
	if err == nil {
		t.Fatal("ensureForRequest() error = nil, want foreground core ACL failure")
	}
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("ensureForRequest() error = %v, want access denied", err)
	}
	if setupErr := windowsRT.workspaceSetupError(); setupErr == "" {
		t.Fatal("workspaceSetupError() = empty, want recorded ACL failure")
	}
}

func TestEnsureForRequestReturnsDenyCarveoutACLFailure(t *testing.T) {
	workspace := t.TempDir()
	gitDir := filepath.Join(workspace, ".git")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(.git) error = %v", err)
	}
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	oldModify := modifyFileDACL
	defer func() { modifyFileDACL = oldModify }()
	modifyFileDACL = func(path string, entries ...acl.Entry) error {
		if pathutil.Key(path) == pathutil.Key(gitDir) {
			return syscall.ERROR_ACCESS_DENIED
		}
		return oldModify(path, entries...)
	}

	_, err = windowsRT.ensureForRequest(context.Background(), sandbox.CommandRequest{Dir: workspace})
	if err == nil {
		t.Fatal("ensureForRequest() error = nil, want deny ACL failure")
	}
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("ensureForRequest() error = %v, want access denied", err)
	}
}

func TestForegroundPolicyExcludesUnrelatedConfiguredWritableRoot(t *testing.T) {
	workspace := t.TempDir()
	extraRoot := filepath.Join(t.TempDir(), "extra-write")
	if err := os.MkdirAll(extraRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll(extraRoot) error = %v", err)
	}
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir(), WritableRoots: []string{extraRoot}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	foreground, err := windowsRT.foregroundPolicyForRequest(sandbox.CommandRequest{Dir: workspace})
	if err != nil {
		t.Fatalf("foregroundPolicyForRequest() error = %v", err)
	}
	full, err := windowsRT.policyForRequest(sandbox.CommandRequest{Dir: workspace})
	if err != nil {
		t.Fatalf("policyForRequest() error = %v", err)
	}
	if containsPath(foreground.WriteRoots, extraRoot) {
		t.Fatalf("foreground WriteRoots = %#v, did not expect unrelated root %q", foreground.WriteRoots, extraRoot)
	}
	if !containsPath(full.WriteRoots, extraRoot) {
		t.Fatalf("full WriteRoots = %#v, want unrelated root %q queued for refresh", full.WriteRoots, extraRoot)
	}
}

func TestApplyPolicyACLsInterruptibleReturnsAppliedWriteRoots(t *testing.T) {
	workspace := t.TempDir()
	missingRoot := filepath.Join(t.TempDir(), "missing")
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	sid := "S-1-5-21-1-2-3-4"
	policy := workspacePolicy{
		WorkspaceRoot:           workspace,
		CommandDir:              workspace,
		WriteRoots:              []string{workspace, missingRoot},
		CapabilitySIDs:          []string{sid},
		WriteRootCapabilitySIDs: map[string]string{pathutil.Normalize(workspace): sid, pathutil.Normalize(missingRoot): sid},
	}

	applied, complete, err := windowsRT.applyPolicyACLsInterruptible(context.Background(), policy)
	if err != nil {
		t.Fatalf("applyPolicyACLsInterruptible() error = %v", err)
	}
	if !complete {
		t.Fatal("applyPolicyACLsInterruptible() complete = false, want true")
	}
	if containsPath(applied.WriteRoots, missingRoot) {
		t.Fatalf("applied WriteRoots = %#v, did not expect missing root %q", applied.WriteRoots, missingRoot)
	}
	if !containsPath(applied.WriteRoots, workspace) {
		t.Fatalf("applied WriteRoots = %#v, want workspace root %q", applied.WriteRoots, workspace)
	}
}

func TestEnsureForRequestCleansStaleDenyACLFromSatisfyingManifest(t *testing.T) {
	workspace := t.TempDir()
	readonly := filepath.Join(workspace, "readonly")
	if err := os.MkdirAll(readonly, 0o700); err != nil {
		t.Fatalf("MkdirAll(readonly) error = %v", err)
	}
	rt, err := New(sandbox.Config{
		CWD:              workspace,
		StateDir:         t.TempDir(),
		ReadOnlySubpaths: []string{"readonly"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	oldPolicy, err := windowsRT.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("ensureForRequestMode() error = %v", err)
	}
	staleDenyEntries := denyEntries(oldPolicy.CapabilitySIDs)
	if len(staleDenyEntries) == 0 {
		t.Fatalf("old policy missing deny entries: %+v", oldPolicy)
	}
	if missing, err := acl.MissingFileDACLEntries(readonly, staleDenyEntries...); err != nil || len(missing) != 0 {
		t.Fatalf("readonly deny ACL entries before foreground ensure = %#v/%v, want present", missing, err)
	}

	windowsRT.cfg.ReadOnlySubpaths = nil
	if _, err := windowsRT.ensureForRequest(context.Background(), sandbox.CommandRequest{Dir: workspace}); err != nil {
		t.Fatalf("ensureForRequest() error = %v", err)
	}
	missing, err := acl.MissingFileDACLEntries(readonly, staleDenyEntries...)
	if err != nil {
		t.Fatalf("MissingFileDACLEntries(after foreground ensure) error = %v", err)
	}
	if len(missing) == 0 {
		t.Fatalf("stale readonly deny ACL entries remained after foreground ensure")
	}
}

func TestEnsureForRequestReturnsManifestWriteError(t *testing.T) {
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	manifestPath := windowsRT.manifestPath()
	if err := os.MkdirAll(manifestPath, 0o700); err != nil {
		t.Fatalf("MkdirAll(manifest path) error = %v", err)
	}

	_, err = windowsRT.ensureForRequest(context.Background(), sandbox.CommandRequest{Dir: workspace})
	if err == nil {
		t.Fatal("ensureForRequest() error = nil, want manifest write error")
	}
	if setupErr := windowsRT.workspaceSetupError(); setupErr == "" {
		t.Fatal("workspaceSetupError() = empty, want recorded manifest write error")
	}
}

func TestPreflightSkipsACLsWhenRepairDisallowed(t *testing.T) {
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	calls := 0
	oldModify := modifyFileDACL
	defer func() { modifyFileDACL = oldModify }()
	modifyFileDACL = func(path string, entries ...acl.Entry) error {
		calls++
		return oldModify(path, entries...)
	}

	if err := windowsRT.Preflight(context.Background(), sandbox.PreflightOptions{}); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("modifyFileDACL calls = %d, want 0", calls)
	}
}

func TestPreflightRefreshesWhenRepairAllowed(t *testing.T) {
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	calls := 0
	oldModify := modifyFileDACL
	defer func() { modifyFileDACL = oldModify }()
	modifyFileDACL = func(path string, entries ...acl.Entry) error {
		calls++
		return oldModify(path, entries...)
	}

	if err := windowsRT.Preflight(context.Background(), sandbox.PreflightOptions{AllowNonElevatedRepair: true}); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if calls == 0 {
		t.Fatal("modifyFileDACL calls = 0, want refresh to prepare ACLs")
	}
}

func TestExistingWritableRootsReturnsUnexpectedStatErrors(t *testing.T) {
	_, err := existingWritableRoots([]string{string([]rune{0})})
	if err == nil {
		t.Fatal("existingWritableRoots() error = nil, want unexpected stat error")
	}
	if !strings.Contains(err.Error(), "inspect writable root") {
		t.Fatalf("existingWritableRoots() error = %v, want path inspection detail", err)
	}
}

func TestPolicySkipsBroadWritableRootsInsteadOfFailing(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("MkdirAll(project) error = %v", err)
	}
	setHomeForWindowsTest(t, home)

	state := t.TempDir()
	rt, err := New(sandbox.Config{
		CWD:           home,
		StateDir:      state,
		WritableRoots: []string{home, project},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)

	policy, err := windowsRT.policyForRequest(sandbox.CommandRequest{
		Dir: home,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			PathRules: []sandbox.PathRule{
				{Path: home, Access: sandbox.PathAccessReadWrite},
				{Path: project, Access: sandbox.PathAccessReadWrite},
			},
		},
	})
	if err != nil {
		t.Fatalf("policyForRequest() error = %v, want broad roots skipped", err)
	}
	if containsPath(policy.WriteRoots, home) {
		t.Fatalf("WriteRoots = %#v, want broad home root skipped", policy.WriteRoots)
	}
	if !containsPath(policy.WriteRoots, project) {
		t.Fatalf("WriteRoots = %#v, want project root retained", policy.WriteRoots)
	}
	if !containsPath(policy.WriteRoots, policy.SandboxEnvRoot) {
		t.Fatalf("WriteRoots = %#v, want sandbox env root %q retained", policy.WriteRoots, policy.SandboxEnvRoot)
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

	async, err := rt.Start(ctx, sandbox.CommandRequest{
		Command: "Write-Output 'first'; Start-Sleep -Milliseconds 50; [Console]::Error.WriteLine('错误'); Start-Sleep -Milliseconds 50; Write-Output '中文'",
		Dir:     workspace,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	})
	if err != nil {
		t.Fatalf("async command start error = %v", err)
	}
	var stdout strings.Builder
	var stderr strings.Builder
	cursor := sandbox.OutputCursor{}
	for {
		observation, err := async.AwaitOutput(ctx, cursor)
		if err != nil {
			t.Fatalf("AwaitOutput(%+v) error = %v", cursor, err)
		}
		out, errOut, nextStdout, nextStderr, err := async.ReadOutput(ctx, cursor.Stdout, cursor.Stderr)
		if err != nil {
			t.Fatalf("ReadOutput(%+v) error = %v", cursor, err)
		}
		next := sandbox.OutputCursor{Stdout: nextStdout, Stderr: nextStderr}
		if next.Stdout < observation.Cursor.Stdout || next.Stderr < observation.Cursor.Stderr {
			t.Fatalf("ReadOutput cursor %+v is behind observation %+v", next, observation.Cursor)
		}
		stdout.Write(out)
		stderr.Write(errOut)
		cursor = next
		if !observation.Status.Running {
			if cursor != observation.Cursor {
				t.Fatalf("terminal ReadOutput cursor = %+v, observation = %+v", cursor, observation.Cursor)
			}
			break
		}
	}
	asyncResult, err := async.Result(ctx)
	if err != nil {
		t.Fatalf("async Result() error = %v; result=%+v", err, asyncResult)
	}
	if stdout.String() != asyncResult.Stdout || stderr.String() != asyncResult.Stderr {
		t.Fatalf(
			"observed output differs from result: stdout=%q/%q stderr=%q/%q",
			stdout.String(),
			asyncResult.Stdout,
			stderr.String(),
			asyncResult.Stderr,
		)
	}
	if !strings.Contains(stdout.String(), "first") || !strings.Contains(stdout.String(), "中文") || !strings.Contains(stderr.String(), "错误") {
		t.Fatalf("async observed output = stdout %q stderr %q, want split non-ASCII streams", stdout.String(), stderr.String())
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

func envValue(env []string, key string) (string, bool) {
	for _, item := range env {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(name, key) {
			return value, true
		}
	}
	return "", false
}

func withFakeSystemOpenSSH(t *testing.T) (string, string) {
	t.Helper()
	systemRoot := filepath.Join(t.TempDir(), "Windows")
	sshPath := filepath.Join(systemRoot, "System32", "OpenSSH", "ssh.exe")
	if err := os.MkdirAll(filepath.Dir(sshPath), 0o700); err != nil {
		t.Fatalf("mkdir OpenSSH dir: %v", err)
	}
	if err := os.WriteFile(sshPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write fake ssh.exe: %v", err)
	}
	t.Setenv("SystemRoot", systemRoot)
	return systemRoot, sshPath
}

func setHomeForWindowsTest(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	volume := filepath.VolumeName(home)
	if volume == "" {
		return
	}
	t.Setenv("HOMEDRIVE", volume)
	t.Setenv("HOMEPATH", strings.TrimPrefix(home, volume))
}

func unsetEnvForTest(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		key := key
		value, ok := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}
