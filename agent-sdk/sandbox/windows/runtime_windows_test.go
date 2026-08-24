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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/acl"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/capability"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
	xwindows "golang.org/x/sys/windows"
)

var testHostReceiptAuthorityRoot string

const (
	hostRuntimeUseHelper     = "CAELIS_HOST_RUNTIME_USE_HELPER"
	hostRuntimeUseStateDir   = "CAELIS_HOST_RUNTIME_USE_STATE_DIR"
	hostRuntimeUseWorkspace  = "CAELIS_HOST_RUNTIME_USE_WORKSPACE"
	hostRuntimeUseReadyFile  = "CAELIS_HOST_RUNTIME_USE_READY_FILE"
	testHostReceiptAuthority = "CAELIS_TEST_HOST_RECEIPT_AUTHORITY"
)

func TestMain(m *testing.M) {
	root := strings.TrimSpace(os.Getenv(testHostReceiptAuthority))
	owned := false
	if root == "" {
		var err error
		root, err = os.MkdirTemp("", "caelis-windows-host-authority-")
		if err != nil {
			panic(err)
		}
		owned = true
	}
	testHostReceiptAuthorityRoot = root
	resolveHostReceiptAuthorityRoot = func(hostUserSID string) (string, error) {
		return hostReceiptAuthorityRootAt(root, hostUserSID)
	}
	code := m.Run()
	if owned {
		_ = os.RemoveAll(root)
	}
	os.Exit(code)
}

func TestRequiresElevatedACLRepairOnlyForTypedWriteAccessDenial(t *testing.T) {
	typed := &acl.DACLWriteAccessError{Path: `C:\workspace`, Err: xwindows.ERROR_ACCESS_DENIED}
	if !requiresElevatedACLRepair(typed) {
		t.Fatal("requiresElevatedACLRepair(typed access denied) = false, want true")
	}
	if requiresElevatedACLRepair(xwindows.ERROR_ACCESS_DENIED) {
		t.Fatal("requiresElevatedACLRepair(untyped access denied) = true, want false")
	}
	if requiresElevatedACLRepair(&acl.DACLWriteAccessError{Path: `C:\workspace`, Err: xwindows.ERROR_INVALID_PARAMETER}) {
		t.Fatal("requiresElevatedACLRepair(non-access-denied write error) = true, want false")
	}
}

func TestResolveStateRootCanonicalizesWindowsPathSpelling(t *testing.T) {
	raw := t.TempDir()
	got, err := resolveStateRoot(raw)
	if err != nil {
		t.Fatalf("resolveStateRoot() error = %v", err)
	}
	want := pathutil.Normalize(raw)
	if !strings.EqualFold(got, want) {
		t.Fatalf("resolveStateRoot(%q) = %q, want canonical %q", raw, got, want)
	}
}

func TestHostRuntimeUseHelperProcess(t *testing.T) {
	if os.Getenv(hostRuntimeUseHelper) != "1" {
		return
	}
	readyFile := os.Getenv(hostRuntimeUseReadyFile)
	report := func(err error) {
		if err == nil {
			_ = os.WriteFile(readyFile, []byte("ready"), 0o600)
			return
		}
		_ = os.WriteFile(readyFile, []byte("error: "+err.Error()), 0o600)
		t.Fatalf("helper setup error = %v", err)
	}
	rt, err := New(sandbox.Config{
		CWD:      os.Getenv(hostRuntimeUseWorkspace),
		StateDir: os.Getenv(hostRuntimeUseStateDir),
	})
	if err != nil {
		report(err)
	}
	windowsRT := rt.(*runtime)
	release, err := windowsRT.beginRuntimeUse()
	if err != nil {
		report(err)
	}
	defer release()
	defer rt.Close()
	if _, err := windowsRT.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: windowsRT.cfg.CWD}, ensureModeBackgroundRefresh); err != nil {
		report(err)
	}
	report(nil)
	for {
		time.Sleep(time.Second)
	}
}

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
	if !desc.Capabilities.TTY {
		t.Fatalf("TTY = false, want restricted-token ConPTY support")
	}
}

func TestRuntimeStartRestrictedConPTYE2E(t *testing.T) {
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E") != "1" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E=1 to run restricted ConPTY e2e")
	}

	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	session, err := rt.Start(context.Background(), sandbox.CommandRequest{
		Command: "if ([Console]::IsInputRedirected) { exit 9 }; $value = [Console]::ReadLine(); Write-Output \"got:$value\"",
		Dir:     workspace,
		TTY:     true,
	})
	if err != nil {
		t.Fatalf("Start(TTY=true) error = %v", err)
	}
	status, err := session.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.SupportsInput {
		t.Fatalf("Status() = %+v, want SupportsInput=true", status)
	}
	if err := session.WriteInput(context.Background(), []byte("demo\r\n")); err != nil {
		t.Fatalf("WriteInput() error = %v", err)
	}
	status, err = session.Wait(context.Background(), 10*time.Second)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Running || status.SupportsInput || status.ExitCode != 0 {
		t.Fatalf("terminal Status() = %+v, want successful terminal state", status)
	}
	result, err := session.Result(context.Background())
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "got:demo") {
		t.Fatalf("stdout = %q, want got:demo", result.Stdout)
	}
	if result.Stderr != "" {
		t.Fatalf("stderr = %q, want merged ConPTY output", result.Stderr)
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

func TestWatchSessionContextTerminatesNonTTYProcessOnTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	terminated := make(chan struct{}, 1)
	go watchSessionContext(ctx, done, func() error {
		terminated <- struct{}{}
		return nil
	})
	select {
	case <-terminated:
	case <-time.After(time.Second):
		t.Fatal("session timeout did not terminate the atomic Job process")
	}
}

func TestNonTTYTimeoutTerminatesContainedDescendant(t *testing.T) {
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E") != "1" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E=1 to run restricted-token Job timeout e2e")
	}
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	pidFile := filepath.Join(workspace, "child.pid")
	command := `$p=Start-Process powershell.exe -ArgumentList @('-NoLogo','-NoProfile','-NonInteractive','-Command','Start-Sleep -Seconds 30') -PassThru; Set-Content -LiteralPath '` + pidFile + `' -Value $p.Id; Wait-Process -Id $p.Id`
	session, err := rt.Start(context.Background(), sandbox.CommandRequest{Command: command, Dir: workspace, Timeout: time.Second})
	if err != nil {
		t.Fatalf("Start(non-TTY timeout) error = %v", err)
	}
	var pid uint64
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(pidFile); readErr == nil {
			pid, err = strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
			if err != nil {
				t.Fatalf("parse child PID: %v", err)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("non-TTY command did not publish descendant PID before timeout")
	}
	status, err := session.Wait(context.Background(), 10*time.Second)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Running {
		t.Fatalf("session remains running after Timeout: %+v", status)
	}
	handle, err := xwindows.OpenProcess(xwindows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // The exited PID may already have been fully reaped.
	}
	defer func() { _ = xwindows.CloseHandle(handle) }()
	if event, err := xwindows.WaitForSingleObject(handle, 0); err != nil || event != xwindows.WAIT_OBJECT_0 {
		t.Fatalf("contained descendant after Timeout = %#x/%v, want exited", event, err)
	}
}

func TestWindowsSessionForcedPublicationDoesNotReleaseRuntimeUseBeforeProcessWait(t *testing.T) {
	t.Parallel()

	released := make(chan struct{})
	session := &windowsSession{
		running:      true,
		done:         make(chan struct{}),
		outputSignal: make(chan struct{}),
		releaseUse:   func() { close(released) },
	}
	session.forceTerminated(errors.New("forced publication"))
	select {
	case <-released:
		t.Fatal("runtime use released before the process wait path became quiescent")
	default:
	}
	session.releaseRuntimeUse()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("runtime use was not released by the process wait path")
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
	if _, err := windowsRT.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh); err != nil {
		t.Fatalf("prepare repair receipts: %v", err)
	}
	manifest, err := windowsRT.readManifest()
	if err != nil {
		t.Fatalf("read repair manifest: %v", err)
	}
	if len(manifest.ManagedReceipts) == 0 {
		t.Fatal("prepared repair manifest has no receipts")
	}
	manifest.ManagedReceipts[0].Applied = false
	manifest.Phase = manifestPhasePrepared
	if err := windowsRT.persistManifest(manifest); err != nil {
		t.Fatalf("persist prepared repair manifest: %v", err)
	}

	oldLauncher := launchElevatedRepairProcess
	defer func() { launchElevatedRepairProcess = oldLauncher }()
	var gotExe string
	var gotCWD string
	var gotArgs []string
	launchElevatedRepairProcess = func(_ context.Context, exe string, args []string, cwd string) (uint32, error) {
		gotExe = exe
		gotCWD = cwd
		gotArgs = append([]string(nil), args...)
		authorityRoot := flagValue(args, "-authority-root")
		requestName := flagValue(args, "-request-name")
		if authorityRoot == "" || requestName == "" || flagValue(args, "-config-file") != "" || flagValue(args, "-result-file") != "" {
			t.Fatalf("repair helper args = %#v, want stable authority and basenames", args)
		}
		data, err := os.ReadFile(filepath.Join(authorityRoot, requestName))
		if err != nil {
			t.Fatalf("read repair request: %v", err)
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
		if request.HostUserSID != windowsRT.hostUserSID || pathutil.Key(request.HostReceiptAuthorityRoot) != pathutil.Key(windowsRT.hostReceiptAuthorityRoot) {
			t.Fatalf("repair request Host authority = %q/%q, want Runtime authority", request.HostUserSID, request.HostReceiptAuthorityRoot)
		}
		for _, name := range []string{requestName, flagValue(args, "-result-name")} {
			info, err := acl.InspectFileDACL(filepath.Join(authorityRoot, name))
			if err != nil {
				t.Fatalf("inspect repair IPC file %s: %v", name, err)
			}
			if !strings.EqualFold(info.OwnerSID, request.HostUserSID) || !info.Protected || !info.HasDACL || info.HasInheritedACE {
				t.Fatalf("repair IPC file %s authority = owner %q protected=%v dacl=%v inherited=%v, want protected Host-owned DACL", name, info.OwnerSID, info.Protected, info.HasDACL, info.HasInheritedACE)
			}
		}
		if err := runInternalRepairHelper(args[1:]); err != nil {
			t.Fatalf("run repair helper: %v", err)
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

func TestLegacyMigrationFailsBeforeReplacementWhenUnknownSIDNeedsElevation(t *testing.T) {
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	policy, err := windowsRT.policyForRequest(sandbox.CommandRequest{Dir: workspace})
	if err != nil {
		t.Fatalf("policyForRequest() error = %v", err)
	}
	legacyEntry := acl.Entry{Principal: "S-1-5-21-424242-434343-444444-454545", Rights: acl.Modify, Mode: acl.Grant, Inherit: true}
	legacyReceipt, err := acl.EnsureExactFileDACLEntry(workspace, legacyEntry)
	if err != nil {
		t.Fatalf("install legacy exact ACE: %v", err)
	}
	defer func() { _, _ = acl.RemoveFileDACLReceipt(workspace, legacyReceipt) }()
	plan := legacyMigrationPlan{Required: true, Receipts: []manifestReceipt{{Path: workspace, Entry: legacyEntry, Receipt: legacyReceipt, Applied: true}}}

	oldProbe := probeFileDACLWriteAccess
	probeFileDACLWriteAccess = func(string) error { return errors.New("access is denied") }
	defer func() { probeFileDACLWriteAccess = oldProbe }()
	if err := windowsRT.preflightLegacyMigrationElevation(plan, policy, receiptEffects(policy)); err == nil || !strings.Contains(err.Error(), "before any ACL effect") {
		t.Fatalf("preflightLegacyMigrationElevation() error = %v, want fail-before-effect diagnostic", err)
	}
	newEntry := acl.Entry{Principal: policy.sidForWriteRoot(workspace), Rights: acl.Modify, Mode: acl.Grant, Inherit: true}
	if count, err := acl.ExactFileDACLEntryCount(workspace, newEntry); err != nil || count != 0 {
		t.Fatalf("replacement exact ACE count after failed preflight = %d/%v, want zero", count, err)
	}
}

func TestInternalRepairVerifiesAllReplacementsBeforeLegacyRetirement(t *testing.T) {
	workspace := t.TempDir()
	external := t.TempDir()
	state := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: state, WritableRoots: []string{external}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	policy, err := windowsRT.policyForRequest(sandbox.CommandRequest{Dir: workspace})
	if err != nil {
		t.Fatalf("policyForRequest() error = %v", err)
	}
	findGrant := func(path string) receiptEffect {
		t.Helper()
		for _, effect := range receiptEffects(policy) {
			if pathutil.Key(effect.Path) == pathutil.Key(path) && effect.Entry.Mode == acl.Grant && effect.Entry.Rights == acl.Modify {
				return effect
			}
		}
		t.Fatalf("policy has no grant effect for %s", path)
		return receiptEffect{}
	}
	workspaceEffect := findGrant(workspace)
	externalEffect := findGrant(external)
	legacyEntry := acl.Entry{
		Principal: capability.DeriveLegacyV1SID(windowsRT.capabilityStorePath(), workspace),
		Rights:    acl.Modify,
		Mode:      acl.Grant,
		Inherit:   true,
	}
	legacyReceipt, err := acl.EnsureExactFileDACLEntry(workspace, legacyEntry)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = acl.RemoveFileDACLReceipt(workspace, legacyReceipt) }()
	workspaceReceipt, err := acl.PrepareExactFileDACLEntry(workspaceEffect.Path, workspaceEffect.Entry)
	if err != nil {
		t.Fatal(err)
	}
	externalReceipt, err := acl.PrepareExactFileDACLEntry(externalEffect.Path, externalEffect.Entry)
	if err != nil {
		t.Fatal(err)
	}
	request := elevatedRepairRequest{
		Config:                   windowsRT.cfg,
		HostUserSID:              windowsRT.hostUserSID,
		HostReceiptAuthorityRoot: windowsRT.hostReceiptAuthorityRoot,
		PolicyHash:               policy.PolicyHash,
		Receipts: []manifestReceipt{
			{Path: workspaceEffect.Path, Entry: workspaceEffect.Entry, Receipt: workspaceReceipt},
			{Path: externalEffect.Path, Entry: externalEffect.Entry, Receipt: externalReceipt},
		},
		RetireReceipts: []manifestReceipt{{Path: legacyReceipt.Path, Entry: legacyEntry, Receipt: legacyReceipt, Applied: true}},
	}
	foreignOwner := request
	foreignOwner.Receipts = append([]manifestReceipt(nil), request.Receipts...)
	foreignOwner.Receipts[1].Receipt.OwnerSID = "S-1-5-18"
	if err := runInternalRepairRequest(foreignOwner); err == nil || !strings.Contains(err.Error(), "not owned by the Host user") {
		t.Fatalf("runInternalRepairRequest(foreign owner) error = %v, want Host-owner rejection", err)
	}
	if count, err := acl.ExactFileDACLEntryCount(workspaceEffect.Path, workspaceEffect.Entry); err != nil || count != 0 {
		t.Fatalf("workspace replacement after foreign-owner rejection = %d/%v, want zero", count, err)
	}
	broken := request
	broken.Receipts = append([]manifestReceipt(nil), request.Receipts...)
	broken.Receipts[1].Receipt.BaselineDACLSHA256 = strings.Repeat("0", 64)
	if err := runInternalRepairRequest(broken); err == nil {
		t.Fatal("runInternalRepairRequest(broken replacement) succeeded")
	}
	if count, err := acl.ExactFileDACLEntryCount(workspace, legacyEntry); err != nil || count != 1 {
		t.Fatalf("legacy ACE after failed replacement verification = %d/%v, want retained", count, err)
	}
	if err := runInternalRepairRequest(request); err != nil {
		t.Fatalf("runInternalRepairRequest() error = %v", err)
	}
	if err := acl.VerifyFileDACLReceipt(workspaceEffect.Path, workspaceReceipt); err != nil {
		t.Fatalf("workspace replacement receipt: %v", err)
	}
	if err := acl.VerifyFileDACLReceipt(externalEffect.Path, externalReceipt); err != nil {
		t.Fatalf("external replacement receipt: %v", err)
	}
	if count, err := acl.ExactFileDACLEntryCount(workspace, legacyEntry); err != nil || count != 0 {
		t.Fatalf("legacy ACE after verified replacements = %d/%v, want retired", count, err)
	}
}

func TestNormalizeRepairReceiptPathsCanonicalizesBothPaths(t *testing.T) {
	path := t.TempDir()
	extendedPath := `\\?\` + path
	receipts := []manifestReceipt{{
		Path: extendedPath,
		Receipt: acl.ACEReceipt{
			Path: extendedPath,
		},
	}}

	got, err := normalizeRepairReceiptPaths(receipts)
	if err != nil {
		t.Fatalf("normalizeRepairReceiptPaths() error = %v", err)
	}
	want := pathutil.Normalize(path)
	if len(got) != 1 || !strings.EqualFold(got[0].Path, want) || !strings.EqualFold(got[0].Receipt.Path, want) {
		t.Fatalf("normalizeRepairReceiptPaths() = %#v, want both paths %q", got, want)
	}
	if receipts[0].Path != extendedPath || receipts[0].Receipt.Path != extendedPath {
		t.Fatal("normalizeRepairReceiptPaths() mutated caller-owned receipts")
	}
}

func TestInternalRepairHelperRejectsCallerSelectedResultPath(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "victim.json")
	const original = "do not overwrite"
	if err := os.WriteFile(victim, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(victim) error = %v", err)
	}
	err := runInternalRepairHelper([]string{
		"-config-file", filepath.Join(t.TempDir(), "request.json"),
		"-result-file", victim,
	})
	if err == nil {
		t.Fatal("runInternalRepairHelper() error = nil, want unknown result-file rejection")
	}
	data, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatalf("ReadFile(victim) error = %v", readErr)
	}
	if got := string(data); got != original {
		t.Fatalf("victim contents = %q, want unchanged", got)
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

	owner, err := acl.InspectFileDACL(workspace)
	if err != nil {
		t.Fatalf("InspectFileDACL(workspace) error = %v", err)
	}
	err = validateElevatedRepairConfig(sandbox.Config{
		CWD:              workspace,
		StateDir:         state,
		RequestedBackend: sandbox.BackendWindows,
		WritableRoots: []string{
			existingOutsideWorkspace,
			missingOutsideWorkspace,
			missingInsideWorkspace,
		},
	}, owner.OwnerSID)
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
	childGit := filepath.Join(workspace, "child", ".git")
	deepGit := filepath.Join(workspace, "container", "child", ".git")
	for _, dir := range []string{commandDir, extraWrite, hidden, outDir, filepath.Join(workspace, ".git"), childGit, deepGit, filepath.Join(workspace, "vendor")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	rt, err := New(sandbox.Config{
		CWD:              workspace,
		StateDir:         t.TempDir(),
		WritableRoots:    []string{workspace, extraWrite},
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
	for _, want := range []string{workspace, extraWrite} {
		if !containsPath(policy.WriteRoots, want) {
			t.Fatalf("WriteRoots = %#v, want %q", policy.WriteRoots, want)
		}
	}
	for _, compacted := range []string{commandDir, outDir} {
		if containsPath(policy.WriteRoots, compacted) {
			t.Fatalf("WriteRoots = %#v, redundant descendant %q was not compacted", policy.WriteRoots, compacted)
		}
	}
	if containsPath(policy.WriteRoots, hidden) || containsPath(policy.DenyWritePaths, hidden) {
		t.Fatalf("policy unexpectedly consumed hidden path %q: %+v", hidden, policy)
	}
	for _, want := range []string{filepath.Join(workspace, ".git"), childGit, filepath.Join(workspace, "vendor")} {
		if !containsPath(policy.DenyWritePaths, want) {
			t.Fatalf("DenyWritePaths = %#v, want %q", policy.DenyWritePaths, want)
		}
	}
	if containsPath(policy.DenyWritePaths, deepGit) {
		t.Fatalf("DenyWritePaths = %#v, did not want depth-two %q", policy.DenyWritePaths, deepGit)
	}
	if len(policy.CapabilitySIDs) == 0 {
		t.Fatalf("CapabilitySIDs empty, want active write SID set")
	}
}

func TestNewRejectsHostAuthorityInsideWritableRoot(t *testing.T) {
	workspace := t.TempDir()
	_, err := New(sandbox.Config{
		CWD:              workspace,
		StateDir:         t.TempDir(),
		HostAuthorityDir: filepath.Join(workspace, ".caelis-host-authority"),
		WritableRoots:    []string{workspace},
	})
	if err == nil || !strings.Contains(err.Error(), "must remain outside sandbox writable root") {
		t.Fatalf("New(Host authority under workspace) error = %v, want writable-root overlap rejection", err)
	}
}

func TestNewAllowsHomeCWDWhenAuthorityIsNotAnEffectiveWritableRoot(t *testing.T) {
	home := t.TempDir()
	setHomeForWindowsTest(t, home)
	authorityBase := filepath.Join(home, "AppData", "Local", "Caelis", "sandbox", "windows", "hosts")

	rt, err := New(sandbox.Config{
		CWD:              home,
		StateDir:         t.TempDir(),
		HostAuthorityDir: authorityBase,
	})
	if err != nil {
		t.Fatalf("New(home CWD) error = %v", err)
	}
	defer rt.Close()
	if windowsRT := rt.(*runtime); containsPath(windowsRT.cfg.WritableRoots, home) {
		t.Fatalf("WritableRoots = %#v, did not want implicit home grant", windowsRT.cfg.WritableRoots)
	}
}

func TestPolicyRejectsDynamicWriteRootCoveringHostAuthority(t *testing.T) {
	workspace := t.TempDir()
	authorityBase := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir(), HostAuthorityDir: authorityBase})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	_, err = windowsRT.policyForRequest(sandbox.CommandRequest{
		Dir: workspace,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			PathRules: []sandbox.PathRule{{
				Path: authorityBase, Access: sandbox.PathAccessReadWrite,
			}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "must remain outside sandbox writable root") {
		t.Fatalf("policyForRequest(authority write rule) error = %v, want overlap rejection", err)
	}
}

func TestNewProtectsHostAuthorityWithExplicitDACL(t *testing.T) {
	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: t.TempDir(), HostAuthorityDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	info, err := acl.InspectFileDACL(windowsRT.hostReceiptAuthorityRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(info.OwnerSID, windowsRT.hostUserSID) {
		t.Fatalf("Host authority owner = %s, want Host user %s", info.OwnerSID, windowsRT.hostUserSID)
	}
	if !info.Protected || !info.HasDACL || info.HasInheritedACE || info.ACECount == 0 {
		t.Fatalf("Host authority DACL = %+v, want protected explicit Host-user entries", info)
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

func TestWorkspaceManifestsAreIsolatedAcrossRuntimeInstances(t *testing.T) {
	stateDir := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	rtA, err := New(sandbox.Config{CWD: workspaceA, StateDir: stateDir})
	if err != nil {
		t.Fatalf("New(workspace A) error = %v", err)
	}
	defer rtA.Close()
	rtB, err := New(sandbox.Config{CWD: workspaceB, StateDir: stateDir})
	if err != nil {
		t.Fatalf("New(workspace B) error = %v", err)
	}
	defer rtB.Close()
	windowsA := rtA.(*runtime)
	windowsB := rtB.(*runtime)

	if windowsA.manifestPath() == windowsB.manifestPath() {
		t.Fatalf("manifest paths = %q, want per-workspace paths", windowsA.manifestPath())
	}
	for name, current := range map[string]*runtime{"A": windowsA, "B": windowsB} {
		if _, err := current.ensureForRequest(context.Background(), sandbox.CommandRequest{Dir: current.cfg.CWD}); err != nil {
			t.Fatalf("ensureForRequest(%s) error = %v", name, err)
		}
	}
	manifestBBefore, err := os.ReadFile(windowsB.manifestPath())
	if err != nil {
		t.Fatalf("ReadFile(workspace B manifest) error = %v", err)
	}
	if err := windowsA.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh(workspace A) error = %v", err)
	}
	manifestBAfter, err := os.ReadFile(windowsB.manifestPath())
	if err != nil {
		t.Fatalf("ReadFile(workspace B manifest after A refresh) error = %v", err)
	}
	if !bytes.Equal(manifestBBefore, manifestBAfter) {
		t.Fatalf("workspace B manifest changed during workspace A refresh\nbefore=%s\nafter=%s", manifestBBefore, manifestBAfter)
	}
	plan := windowsA.cleanupPlan()
	if containsPath(plan.LegacyPaths, windowsB.manifestPath()) {
		t.Fatalf("workspace A cleanup paths = %#v, must not include workspace B manifest", plan.LegacyPaths)
	}
}

func TestRefreshMigratesLegacyManifestAndCleansStaleDenyACL(t *testing.T) {
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
	legacyWorkspaceSID := "S-1-5-21-100-200-300-4101"
	legacyEnvSID := "S-1-5-21-100-200-300-4102"
	legacyStore := capability.Store{
		WorkspaceByCWD: map[string]string{pathutil.Key(workspace): legacyWorkspaceSID},
		WritableRootByPath: map[string]string{
			pathutil.Key(windowsRT.sandboxEnvRoot(workspace)): legacyEnvSID,
		},
	}
	legacyStoreData, err := json.Marshal(legacyStore)
	if err != nil {
		t.Fatalf("Marshal(legacy Store) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(windowsRT.capabilityStorePath()), 0o700); err != nil {
		t.Fatalf("MkdirAll(capability Store) error = %v", err)
	}
	if err := os.WriteFile(windowsRT.capabilityStorePath(), legacyStoreData, 0o600); err != nil {
		t.Fatalf("WriteFile(legacy Store) error = %v", err)
	}
	legacyPolicy, err := windowsRT.policyForRequestMode(sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("policyForRequestMode(legacy) error = %v", err)
	}
	legacyPolicy.CapabilitySIDs = []string{legacyWorkspaceSID, legacyEnvSID}
	legacyPolicy.WriteRootCapabilitySIDs = map[string]string{
		pathutil.Normalize(workspace):                   legacyWorkspaceSID,
		pathutil.Normalize(legacyPolicy.SandboxEnvRoot): legacyEnvSID,
	}
	windowsRT.stateCoordinator.aclMu.Lock()
	for _, effect := range receiptEffects(legacyPolicy) {
		if err := acl.ModifyFileDACL(effect.Path, effect.Entry); err != nil {
			windowsRT.stateCoordinator.aclMu.Unlock()
			t.Fatalf("ModifyFileDACL(legacy) error = %v", err)
		}
	}
	legacyManifest := workspaceManifestForPolicy(legacyPolicy)
	legacyManifest.Version = 1
	legacyManifest.Phase = ""
	if err := persistWorkspaceManifest(windowsRT.legacyManifestPath(), legacyManifest); err != nil {
		windowsRT.stateCoordinator.aclMu.Unlock()
		t.Fatalf("persistWorkspaceManifest(legacy) error = %v", err)
	}
	windowsRT.stateCoordinator.aclMu.Unlock()
	if _, err := os.Stat(windowsRT.manifestPath()); !os.IsNotExist(err) {
		t.Fatalf("per-workspace manifest stat before refresh = %v, want missing", err)
	}
	legacyDeny := []acl.Entry{{
		Principal: legacyWorkspaceSID,
		Rights:    acl.Write,
		Mode:      acl.Deny,
		Inherit:   true,
	}}
	if missing, err := acl.MissingFileDACLEntries(readonly, legacyDeny...); err != nil || len(missing) != 0 {
		t.Fatalf("legacy deny ACL before refresh = %#v/%v, want present", missing, err)
	}

	windowsRT.cfg.ReadOnlySubpaths = nil
	if err := windowsRT.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if _, err := os.Stat(windowsRT.legacyManifestPath()); !os.IsNotExist(err) {
		t.Fatalf("legacy manifest stat after refresh = %v, want removed", err)
	}
	manifest, err := windowsRT.readManifest()
	if err != nil {
		t.Fatalf("readManifest() error = %v", err)
	}
	if len(manifest.LegacyACEs) != 0 || manifest.LegacyMigrationPrepared {
		t.Fatalf("migrated manifest = %+v, want legacy evidence consumed after exact retirement", manifest)
	}
	if missing, err := acl.MissingFileDACLEntries(readonly, legacyDeny...); err != nil || len(missing) == 0 {
		t.Fatalf("legacy deny ACL after refresh = %#v/%v, want exact legacy ACE removed", missing, err)
	}
}

func TestRefreshPreservesManifestReceiptWhenStaleACLCleanupFails(t *testing.T) {
	workspace := t.TempDir()
	staleRoot := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	policy, err := windowsRT.policyForRequestMode(sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("policyForRequestMode() error = %v", err)
	}
	staleACE := manifestACE{
		Path:      staleRoot,
		Principal: "not-a-valid-windows-principal",
		Mode:      string(acl.Deny),
		Rights:    string(acl.Write),
		Inherit:   true,
	}
	manifest := workspaceManifestForPolicy(policy)
	manifest.Version = 1
	manifest.Phase = ""
	manifest.ACEs = append(manifest.ACEs, staleACE)
	manifest.DenyWritePaths = append(manifest.DenyWritePaths, staleRoot)
	windowsRT.stateCoordinator.aclMu.Lock()
	if err := windowsRT.persistManifest(manifest); err != nil {
		windowsRT.stateCoordinator.aclMu.Unlock()
		t.Fatalf("persistManifest() error = %v", err)
	}
	err = windowsRT.refreshACLStateLocked(context.Background(), policy)
	windowsRT.stateCoordinator.aclMu.Unlock()
	if err == nil {
		t.Fatalf("refreshACLStateLocked() error = nil, want fail-closed unproven legacy residue")
	}
	after, readErr := windowsRT.readManifest()
	if readErr != nil {
		t.Fatalf("readManifest() error = %v", readErr)
	}
	if !containsManifestACE(after.ACEs, staleACE) {
		t.Fatalf("manifest ACEs = %#v, want unproven legacy manifest unchanged", after.ACEs)
	}
}

func TestActiveRuntimePoliciesShareWorkspaceManifestWithoutRevocation(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	extraA := t.TempDir()
	extraB := t.TempDir()
	rtA, err := New(sandbox.Config{CWD: workspace, StateDir: stateDir, WritableRoots: []string{extraA}})
	if err != nil {
		t.Fatalf("New(runtime A) error = %v", err)
	}
	defer rtA.Close()
	rtB, err := New(sandbox.Config{CWD: workspace, StateDir: stateDir, WritableRoots: []string{extraB}})
	if err != nil {
		t.Fatalf("New(runtime B) error = %v", err)
	}
	defer rtB.Close()
	windowsA := rtA.(*runtime)
	windowsB := rtB.(*runtime)

	policyA, err := windowsA.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("ensureForRequestMode(runtime A) error = %v", err)
	}
	if _, err := windowsB.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh); err != nil {
		t.Fatalf("ensureForRequestMode(runtime B) error = %v", err)
	}
	manifest, err := windowsA.readManifest()
	if err != nil {
		t.Fatalf("readManifest(active runtimes) error = %v", err)
	}
	if !containsPath(manifest.WriteRoots, extraA) || !containsPath(manifest.WriteRoots, extraB) {
		t.Fatalf("active manifest WriteRoots = %#v, want both Runtime policies", manifest.WriteRoots)
	}
	entriesA := allowEntries(policyA.sidForWriteRoot(extraA))
	if missing, err := acl.MissingFileDACLEntries(extraA, entriesA...); err != nil || len(missing) != 0 {
		t.Fatalf("runtime A ACL after runtime B ensure = %#v/%v, want preserved", missing, err)
	}

	if err := rtB.Close(); err != nil {
		t.Fatalf("Close(runtime B) error = %v", err)
	}
	if err := windowsA.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh(runtime A after B close) error = %v", err)
	}
	manifest, err = windowsA.readManifest()
	if err != nil {
		t.Fatalf("readManifest(after B close) error = %v", err)
	}
	if !containsPath(manifest.WriteRoots, extraA) || containsPath(manifest.WriteRoots, extraB) {
		t.Fatalf("pruned manifest WriteRoots = %#v, want only active Runtime extra root %q", manifest.WriteRoots, extraA)
	}
}

func TestSharedExternalRootReceiptSurvivesCrossStateDirRetirement(t *testing.T) {
	externalRoot := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	rtA, err := New(sandbox.Config{CWD: workspaceA, StateDir: t.TempDir(), WritableRoots: []string{externalRoot}})
	if err != nil {
		t.Fatalf("New(runtime A) error = %v", err)
	}
	defer rtA.Close()
	rtB, err := New(sandbox.Config{CWD: workspaceB, StateDir: t.TempDir(), WritableRoots: []string{externalRoot}})
	if err != nil {
		t.Fatalf("New(runtime B) error = %v", err)
	}
	defer rtB.Close()
	windowsA := rtA.(*runtime)
	windowsB := rtB.(*runtime)
	policyA, err := windowsA.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspaceA}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("ensureForRequestMode(runtime A) error = %v", err)
	}
	policyB, err := windowsB.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspaceB}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("ensureForRequestMode(runtime B) error = %v", err)
	}
	if policyA.sidForWriteRoot(externalRoot) != policyB.sidForWriteRoot(externalRoot) {
		t.Fatalf("external root SIDs differ across StateDirs: %q vs %q", policyA.sidForWriteRoot(externalRoot), policyB.sidForWriteRoot(externalRoot))
	}
	entry := allowEntries(policyA.sidForWriteRoot(externalRoot))[0]
	if err := windowsA.withHostReceiptLedger(func(ledger *hostReceiptLedger) error {
		index := hostReceiptIndex(*ledger, pathutil.Normalize(externalRoot), entry)
		if index < 0 {
			t.Fatal("Host ledger has no cross-StateDir external-root receipt")
		}
		if got := len(ledger.Effects[index].References); got != 2 {
			t.Fatalf("cross-StateDir receipt references = %d, want 2", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("read Host receipt ledger error = %v", err)
	}
	if missing, err := acl.MissingFileDACLEntries(externalRoot, entry); err != nil || len(missing) != 0 {
		t.Fatalf("shared external ACL before retirement = %#v/%v", missing, err)
	}

	if err := windowsA.resetWorkspaceReceipts(context.Background()); err != nil {
		t.Fatalf("resetWorkspaceReceipts(runtime A) error = %v", err)
	}
	if missing, err := acl.MissingFileDACLEntries(externalRoot, entry); err != nil || len(missing) != 0 {
		t.Fatalf("shared external ACL after first retirement = %#v/%v, want preserved", missing, err)
	}
	if err := windowsB.resetWorkspaceReceipts(context.Background()); err != nil {
		t.Fatalf("resetWorkspaceReceipts(runtime B) error = %v", err)
	}
	if missing, err := acl.MissingFileDACLEntries(externalRoot, entry); err != nil || len(missing) == 0 {
		t.Fatalf("shared external ACL after final retirement = %#v/%v, want removed", missing, err)
	}
}

func TestCrossProcessRuntimeUseBlocksResetAndEnvironmentGC(t *testing.T) {
	stateDir := t.TempDir()
	helperWorkspace := t.TempDir()
	readyFile := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostRuntimeUseHelperProcess$")
	cmd.Env = append(os.Environ(),
		hostRuntimeUseHelper+"=1",
		hostRuntimeUseStateDir+"="+stateDir,
		hostRuntimeUseWorkspace+"="+helperWorkspace,
		hostRuntimeUseReadyFile+"="+readyFile,
		testHostReceiptAuthority+"="+testHostReceiptAuthorityRoot,
	)
	var helperOutput bytes.Buffer
	cmd.Stdout = &helperOutput
	cmd.Stderr = &helperOutput
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process error = %v", err)
	}
	waited := false
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		if !waited {
			_ = cmd.Wait()
		}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, err := os.ReadFile(readyFile)
		if err == nil {
			if got := string(data); got != "ready" {
				t.Fatalf("helper readiness = %q; output=%s", got, helperOutput.String())
			}
			break
		}
		if !os.IsNotExist(err) {
			t.Fatalf("read helper readiness error = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper process did not become ready; output=%s", helperOutput.String())
		}
		time.Sleep(25 * time.Millisecond)
	}

	rt, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatalf("New(parent runtime) error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	if err := windowsRT.Reset(context.Background()); !errors.Is(err, errSandboxStateBusy) {
		t.Fatalf("Reset() with foreign process use error = %v, want busy", err)
	}
	helperEnvRoot := windowsRT.sandboxEnvRoot(helperWorkspace)
	if err := windowsRT.retireAndRemoveSandboxEnv(context.Background(), helperEnvRoot); !errors.Is(err, errSandboxStateBusy) {
		t.Fatalf("retireAndRemoveSandboxEnv() with foreign process use error = %v, want busy", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper process error = %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed helper process exited without an error")
	}
	waited = true
	if err := windowsRT.retireAndRemoveSandboxEnv(context.Background(), helperEnvRoot); err != nil {
		t.Fatalf("retireAndRemoveSandboxEnv() after helper death error = %v; output=%s", err, helperOutput.String())
	}
	if _, err := os.Stat(helperEnvRoot); !os.IsNotExist(err) {
		t.Fatalf("helper sandbox environment stat after recovery = %v, want missing", err)
	}
	if err := windowsRT.Reset(context.Background()); err != nil {
		t.Fatalf("Reset() after helper death error = %v", err)
	}
}

func TestDeletingEnvironmentRecoversBeforeRuntimeUse(t *testing.T) {
	workspace := t.TempDir()
	stateDir := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: stateDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	windowsRT := rt.(*runtime)
	if _, err := windowsRT.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh); err != nil {
		t.Fatalf("ensureForRequestMode() error = %v", err)
	}
	envRoot := windowsRT.registeredEnvRoot
	if err := windowsRT.withHostReceiptLedger(func(ledger *hostReceiptLedger) error {
		manifest, err := windowsRT.readManifest()
		if err != nil {
			return err
		}
		return windowsRT.retireEnvironmentReceiptsTransaction(context.Background(), windowsRT.manifestPath(), &manifest, envRoot, ledger)
	}); err != nil {
		t.Fatalf("prepare deleting environment transaction error = %v", err)
	}
	if _, err := os.Stat(envRoot); err != nil {
		t.Fatalf("sandbox environment after simulated crash stat = %v, want retained", err)
	}
	manifest, err := windowsRT.readManifest()
	if err != nil {
		t.Fatalf("read deleting manifest error = %v", err)
	}
	if manifest.Phase != manifestPhaseDeleting || pathutil.Key(manifest.SandboxEnvRoot) != pathutil.Key(envRoot) || pathutil.Key(manifest.DeletingEnvRoot) != pathutil.Key(envRoot) {
		t.Fatalf("deleting manifest = %+v, want durable source and target", manifest)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("Close(first runtime) error = %v", err)
	}

	reopened, err := New(sandbox.Config{CWD: workspace, StateDir: stateDir})
	if err != nil {
		t.Fatalf("New(reopened runtime) error = %v", err)
	}
	defer reopened.Close()
	reopenedWindows := reopened.(*runtime)
	release, err := reopenedWindows.beginRuntimeUse()
	if err != nil {
		t.Fatalf("beginRuntimeUse() recovery error = %v", err)
	}
	defer release()
	if _, err := os.Stat(envRoot); !os.IsNotExist(err) {
		t.Fatalf("sandbox environment after deleting recovery stat = %v, want missing", err)
	}
	manifest, err = reopenedWindows.readManifest()
	if err != nil {
		t.Fatalf("read recovered manifest error = %v", err)
	}
	if manifest.Phase != manifestPhaseActive || manifest.SandboxEnvRoot != "" || manifest.DeletingEnvRoot != "" {
		t.Fatalf("recovered manifest = %+v, want finalized deletion", manifest)
	}
	if _, err := reopenedWindows.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh); err != nil {
		t.Fatalf("ensureForRequestMode() after deleting recovery error = %v", err)
	}
}

func TestActiveManifestRejectsForeignUnappliedReceipt(t *testing.T) {
	workspace := t.TempDir()
	foreignRoot := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	policy, err := windowsRT.policyForRequestMode(sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("policyForRequestMode() error = %v", err)
	}
	entry := allowEntries(policy.sidForWriteRoot(workspace))[0]
	receipt, err := acl.PrepareExactFileDACLEntry(foreignRoot, entry)
	if err != nil {
		t.Fatalf("PrepareExactFileDACLEntry() error = %v", err)
	}
	manifest := workspaceManifestForPolicy(policy)
	manifest.ManagedReceipts = []manifestReceipt{{Path: foreignRoot, Entry: entry, Receipt: receipt}}
	if err := windowsRT.persistManifest(manifest); err != nil {
		t.Fatalf("persist tampered active manifest error = %v", err)
	}
	if _, err := windowsRT.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh); err == nil || !strings.Contains(err.Error(), "outside the current validated policy") {
		t.Fatalf("ensureForRequestMode(tampered active manifest) error = %v, want foreign receipt rejection", err)
	}
	if missing, err := acl.MissingFileDACLEntries(foreignRoot, entry); err != nil || len(missing) == 0 {
		t.Fatalf("foreign ACL after rejected manifest = %#v/%v, want absent", missing, err)
	}
}

func TestManifestRejectsReceiptEntryMismatch(t *testing.T) {
	root := t.TempDir()
	entry := acl.Entry{Principal: "S-1-5-21-100-200-300-5101", Rights: acl.Modify, Mode: acl.Grant, Inherit: true}
	receipt, err := acl.PrepareExactFileDACLEntry(root, entry)
	if err != nil {
		t.Fatalf("PrepareExactFileDACLEntry() error = %v", err)
	}
	manifest := workspaceManifest{
		Version:       workspaceManifestVersion,
		WorkspaceRoot: root,
		Phase:         manifestPhasePrepared,
		ManagedReceipts: []manifestReceipt{{
			Path:    root,
			Entry:   acl.Entry{Principal: "S-1-5-21-100-200-300-5102", Rights: acl.Modify, Mode: acl.Grant, Inherit: true},
			Receipt: receipt,
		}},
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := persistWorkspaceManifest(path, manifest); err != nil {
		t.Fatalf("persistWorkspaceManifest() error = %v", err)
	}
	if _, err := readWorkspaceManifest(path); err == nil {
		t.Fatal("readWorkspaceManifest() error = nil, want receipt Entry mismatch rejection")
	}
}

func TestRetiringReceiptRevivesWithoutReleasingHostReference(t *testing.T) {
	workspace := t.TempDir()
	rootA := t.TempDir()
	rootB := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir(), WritableRoots: []string{rootA}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	policyA, err := windowsRT.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("ensure policy A error = %v", err)
	}
	windowsRT.cfg.WritableRoots = []string{rootB}
	policyB, err := windowsRT.policyForRequestMode(sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("policy B error = %v", err)
	}
	windowsRT.stateCoordinator.aclMu.Lock()
	err = windowsRT.activateReceiptPolicyLocked(context.Background(), policyB, false)
	windowsRT.stateCoordinator.aclMu.Unlock()
	if err != nil {
		t.Fatalf("activate policy B without retirement error = %v", err)
	}
	entryA := allowEntries(policyA.sidForWriteRoot(rootA))[0]
	manifest, err := windowsRT.readManifest()
	if err != nil {
		t.Fatalf("read manifest B error = %v", err)
	}
	rootAKey := receiptEffectKey(pathutil.Normalize(rootA), entryA)
	managedA := receiptIndex(manifest.ManagedReceipts, rootAKey)
	if managedA < 0 {
		t.Fatalf("manifest B managed receipts = %+v, want preserved root A before retirement", manifest.ManagedReceipts)
	}
	manifest.RetiringReceipts = appendReceipt(manifest.RetiringReceipts, manifest.ManagedReceipts[managedA])
	manifest.ManagedReceipts = append(manifest.ManagedReceipts[:managedA], manifest.ManagedReceipts[managedA+1:]...)
	if err := windowsRT.persistManifest(manifest); err != nil {
		t.Fatalf("persist interrupted A-to-B transition error = %v", err)
	}
	manifest, err = windowsRT.readManifest()
	if err != nil {
		t.Fatalf("read interrupted A-to-B manifest error = %v", err)
	}
	if receiptIndex(manifest.RetiringReceipts, rootAKey) < 0 {
		t.Fatalf("manifest B retiring receipts = %+v, want root A", manifest.RetiringReceipts)
	}
	windowsRT.cfg.WritableRoots = []string{rootA}
	policyA, err = windowsRT.policyForRequestMode(sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("policy A restore error = %v", err)
	}
	windowsRT.stateCoordinator.aclMu.Lock()
	err = windowsRT.activateReceiptPolicyLocked(context.Background(), policyA, true)
	windowsRT.stateCoordinator.aclMu.Unlock()
	if err != nil {
		t.Fatalf("reactivate policy A error = %v", err)
	}
	manifest, err = windowsRT.readManifest()
	if err != nil {
		t.Fatalf("read reactivated manifest error = %v", err)
	}
	if receiptIndex(manifest.ManagedReceipts, rootAKey) < 0 || receiptIndex(manifest.RetiringReceipts, rootAKey) >= 0 {
		t.Fatalf("reactivated manifest receipts = managed:%+v retiring:%+v, want root A managed only", manifest.ManagedReceipts, manifest.RetiringReceipts)
	}
	if missing, err := acl.MissingFileDACLEntries(rootA, entryA); err != nil || len(missing) != 0 {
		t.Fatalf("root A ACL after reactivation = %#v/%v, want present", missing, err)
	}
}

func TestHostReceiptReleaseResumesAfterReferenceCommit(t *testing.T) {
	workspace := t.TempDir()
	externalRoot := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir(), WritableRoots: []string{externalRoot}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	policy, err := windowsRT.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("ensureForRequestMode() error = %v", err)
	}
	entry := allowEntries(policy.sidForWriteRoot(externalRoot))[0]
	reference := hostReceiptReferenceForManifest(windowsRT.manifestPath())
	if err := windowsRT.withHostReceiptLedger(func(ledger *hostReceiptLedger) error {
		index := hostReceiptIndex(*ledger, pathutil.Normalize(externalRoot), entry)
		if index < 0 {
			t.Fatalf("Host ledger has no external-root receipt")
		}
		refs := ledger.Effects[index].References[:0]
		for _, candidate := range ledger.Effects[index].References {
			if candidate != reference {
				refs = append(refs, candidate)
			}
		}
		ledger.Effects[index].References = refs
		return windowsRT.persistHostReceiptLedger(*ledger)
	}); err != nil {
		t.Fatalf("persist simulated release reference commit error = %v", err)
	}
	if err := windowsRT.resetWorkspaceReceipts(context.Background()); err != nil {
		t.Fatalf("resetWorkspaceReceipts(retry) error = %v", err)
	}
	if missing, err := acl.MissingFileDACLEntries(externalRoot, entry); err != nil || len(missing) == 0 {
		t.Fatalf("external ACL after resumed release = %#v/%v, want removed", missing, err)
	}
}

func TestCanceledReceiptResetDoesNotPublishRetirement(t *testing.T) {
	workspace := t.TempDir()
	externalRoot := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir(), WritableRoots: []string{externalRoot}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	policy, err := windowsRT.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("ensureForRequestMode() error = %v", err)
	}
	before, err := os.ReadFile(windowsRT.manifestPath())
	if err != nil {
		t.Fatalf("ReadFile(manifest before reset) error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := windowsRT.resetWorkspaceReceipts(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("resetWorkspaceReceipts(canceled) error = %v, want context canceled", err)
	}
	after, err := os.ReadFile(windowsRT.manifestPath())
	if err != nil {
		t.Fatalf("ReadFile(manifest after reset) error = %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("manifest changed during canceled reset\nbefore=%s\nafter=%s", before, after)
	}
	entry := allowEntries(policy.sidForWriteRoot(externalRoot))[0]
	if missing, err := acl.MissingFileDACLEntries(externalRoot, entry); err != nil || len(missing) != 0 {
		t.Fatalf("external ACL after canceled reset = %#v/%v, want present", missing, err)
	}
}

func TestRefreshManifestTransactionPreservesClosingRuntimeReceipt(t *testing.T) {
	workspace := t.TempDir()
	extraA := t.TempDir()
	extraB := t.TempDir()
	stateDir := t.TempDir()
	rtA, err := New(sandbox.Config{CWD: workspace, StateDir: stateDir, WritableRoots: []string{extraA}})
	if err != nil {
		t.Fatalf("New(runtime A) error = %v", err)
	}
	defer rtA.Close()
	rtB, err := New(sandbox.Config{CWD: workspace, StateDir: stateDir, WritableRoots: []string{extraB}})
	if err != nil {
		t.Fatalf("New(runtime B) error = %v", err)
	}
	windowsA := rtA.(*runtime)
	windowsB := rtB.(*runtime)
	for name, current := range map[string]*runtime{"A": windowsA, "B": windowsB} {
		if _, err := current.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh); err != nil {
			t.Fatalf("ensureForRequestMode(%s) error = %v", name, err)
		}
	}
	policyA, err := windowsA.policyForRequestMode(sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("policyForRequestMode(A) error = %v", err)
	}
	windowsA.stateCoordinator.aclMu.Lock()
	closed := make(chan error, 1)
	go func() { closed <- rtB.Close() }()
	select {
	case err := <-closed:
		windowsA.stateCoordinator.aclMu.Unlock()
		t.Fatalf("Close(runtime B) completed inside manifest transaction: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := windowsA.refreshACLStateLocked(context.Background(), policyA); err != nil {
		windowsA.stateCoordinator.aclMu.Unlock()
		t.Fatalf("refreshACLStateLocked() error = %v", err)
	}
	windowsA.stateCoordinator.aclMu.Unlock()
	if err := <-closed; err != nil {
		t.Fatalf("Close(runtime B) error = %v", err)
	}
	manifest, err := windowsA.readManifest()
	if err != nil {
		t.Fatalf("readManifest() error = %v", err)
	}
	if !containsPath(manifest.WriteRoots, extraA) || !containsPath(manifest.WriteRoots, extraB) {
		t.Fatalf("manifest WriteRoots = %#v, want pre-existing and concurrent-use policies", manifest.WriteRoots)
	}
}

func TestRuntimeInstancesShareACLMutationCoordinator(t *testing.T) {
	stateDir := t.TempDir()
	rtA, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatalf("New(workspace A) error = %v", err)
	}
	defer rtA.Close()
	rtB, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatalf("New(workspace B) error = %v", err)
	}
	defer rtB.Close()
	windowsA := rtA.(*runtime)
	windowsB := rtB.(*runtime)
	if windowsA.stateCoordinator != windowsB.stateCoordinator {
		t.Fatal("runtime coordinators differ, want one StateDir-scoped coordinator")
	}

	windowsA.stateCoordinator.aclMu.Lock()
	result := make(chan error, 1)
	go func() {
		_, err := windowsB.ensureForRequest(context.Background(), sandbox.CommandRequest{Dir: windowsB.cfg.CWD})
		result <- err
	}()
	select {
	case err := <-result:
		windowsA.stateCoordinator.aclMu.Unlock()
		t.Fatalf("ensureForRequest completed without shared ACL serialization: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	windowsA.stateCoordinator.aclMu.Unlock()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("ensureForRequest() error = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ensureForRequest remained blocked after shared ACL coordinator release")
	}
}

func TestEnvCacheRemovalSerializesRuntimeRegistration(t *testing.T) {
	coordinator := &sandboxStateCoordinator{activeEnvRoots: map[string]int{}}
	envRoot := filepath.Join(t.TempDir(), "env")
	otherRoot := filepath.Join(t.TempDir(), "other")
	removeStarted := make(chan struct{})
	allowRemove := make(chan struct{})
	removed := make(chan error, 1)
	go func() {
		_, err := coordinator.withUnusedEnvRoot(envRoot, func() error {
			close(removeStarted)
			<-allowRemove
			return nil
		})
		removed <- err
	}()
	<-removeStarted
	responsive := make(chan bool, 1)
	go func() { responsive <- coordinator.protectsEnvRoot(otherRoot) }()
	select {
	case <-responsive:
	case <-time.After(time.Second):
		close(allowRemove)
		t.Fatal("coordinator state lock remained held during cache removal")
	}

	registered := make(chan error, 1)
	go func() {
		registered <- coordinator.registerRuntime(envRoot)
	}()
	select {
	case err := <-registered:
		close(allowRemove)
		t.Fatalf("registerRuntime completed during cache removal: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	aclResponsive := make(chan struct{}, 1)
	go func() {
		coordinator.aclMu.Lock()
		aclResponsive <- struct{}{}
		coordinator.aclMu.Unlock()
	}()
	select {
	case <-aclResponsive:
	case <-time.After(time.Second):
		close(allowRemove)
		t.Fatal("ACL coordinator lock remained held while registration waited for cache removal")
	}
	close(allowRemove)
	if err := <-removed; err != nil {
		t.Fatalf("withUnusedEnvRoot() error = %v", err)
	}
	select {
	case err := <-registered:
		if err != nil {
			t.Fatalf("registerRuntime() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registerRuntime remained blocked after cache removal")
	}
	coordinator.unregisterRuntime(envRoot)
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
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("unproven old env stat error = %v, want fail-closed preservation", err)
	}
}

func TestCleanupSandboxCachesPreservesOtherRuntimeEnvRoot(t *testing.T) {
	stateDir := t.TempDir()
	rtA, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatalf("New(workspace A) error = %v", err)
	}
	defer rtA.Close()
	rtB, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatalf("New(workspace B) error = %v", err)
	}
	defer rtB.Close()
	windowsA := rtA.(*runtime)
	windowsB := rtB.(*runtime)
	for name, current := range map[string]*runtime{"A": windowsA, "B": windowsB} {
		if _, err := current.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: current.cfg.CWD}, ensureModeBackgroundRefresh); err != nil {
			t.Fatalf("ensureForRequestMode(%s) error = %v", name, err)
		}
	}
	for _, root := range []string{windowsA.registeredEnvRoot, windowsB.registeredEnvRoot} {
		if err := os.MkdirAll(filepath.Join(root, "cache"), 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", root, err)
		}
		if err := os.WriteFile(filepath.Join(root, "cache", "cache.bin"), []byte("cache"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", root, err)
		}
		stale := time.Now().Add(-(windowsCacheMaxAge + time.Hour))
		if err := os.Chtimes(root, stale, stale); err != nil {
			t.Fatalf("Chtimes(%s) error = %v", root, err)
		}
	}

	if err := windowsA.cleanupSandboxCaches(context.Background(), windowsA.registeredEnvRoot); err != nil {
		t.Fatalf("cleanupSandboxCaches(active runtimes) error = %v", err)
	}
	if _, err := os.Stat(windowsB.registeredEnvRoot); err != nil {
		t.Fatalf("other active Runtime env stat error = %v, want preserved", err)
	}
	if err := rtB.Close(); err != nil {
		t.Fatalf("Close(workspace B) error = %v", err)
	}
	if err := windowsA.cleanupSandboxCaches(context.Background(), windowsA.registeredEnvRoot); err != nil {
		t.Fatalf("cleanupSandboxCaches(after close) error = %v", err)
	}
	if _, err := os.Stat(windowsB.registeredEnvRoot); !os.IsNotExist(err) {
		t.Fatalf("closed proven Runtime env stat error = %v, want exact-receipt retirement and removal", err)
	}
}

func TestReceiptManifestRotationRemovesOnlyOwnedExactACE(t *testing.T) {
	workspace := t.TempDir()
	staleRoot := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir(), WritableRoots: []string{staleRoot}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	oldPolicy, err := windowsRT.ensureForRequestMode(context.Background(), sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("ensureForRequestMode(old) error = %v", err)
	}
	managed := acl.Entry{Principal: oldPolicy.sidForWriteRoot(staleRoot), Rights: acl.Modify, Mode: acl.Grant, Inherit: true}
	thirdParty := acl.Entry{Principal: "S-1-5-21-9-8-7-6", Rights: acl.ReadExecute, Mode: acl.Grant, Inherit: true}
	if err := acl.ModifyFileDACL(staleRoot, thirdParty); err != nil {
		t.Fatalf("ModifyFileDACL(third party) error = %v", err)
	}
	windowsRT.cfg.WritableRoots = nil
	if err := windowsRT.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh(rotation) error = %v", err)
	}
	if missing, err := acl.MissingFileDACLEntries(staleRoot, managed); err != nil || len(missing) == 0 {
		t.Fatalf("managed stale ACE after rotation = %#v/%v, want removed", missing, err)
	}
	if missing, err := acl.MissingFileDACLEntries(staleRoot, thirdParty); err != nil || len(missing) != 0 {
		t.Fatalf("third-party ACE after rotation = %#v/%v, want preserved", missing, err)
	}
}

func TestReceiptManifestRecoversEffectBeforeAppliedCommit(t *testing.T) {
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	policy, err := windowsRT.policyForRequestMode(sandbox.CommandRequest{Dir: workspace}, ensureModeBackgroundRefresh)
	if err != nil {
		t.Fatalf("policyForRequestMode() error = %v", err)
	}
	effect := receiptEffects(policy)[0]
	receipt, err := acl.PrepareExactFileDACLEntry(effect.Path, effect.Entry)
	if err != nil {
		t.Fatalf("PrepareExactFileDACLEntry() error = %v", err)
	}
	manifest := workspaceManifestForPolicy(policy)
	manifest.Phase = manifestPhasePrepared
	manifest.ManagedReceipts = []manifestReceipt{{Path: effect.Path, Entry: effect.Entry, Receipt: receipt}}
	if err := windowsRT.persistManifest(manifest); err != nil {
		t.Fatalf("persistManifest(prepared) error = %v", err)
	}
	if err := acl.EnsureFileDACLReceipt(effect.Path, receipt); err != nil {
		t.Fatalf("EnsureFileDACLReceipt(crash effect) error = %v", err)
	}
	windowsRT.stateCoordinator.aclMu.Lock()
	err = windowsRT.activateReceiptPolicyLocked(context.Background(), policy, true)
	windowsRT.stateCoordinator.aclMu.Unlock()
	if err != nil {
		t.Fatalf("activateReceiptPolicyLocked(recovery) error = %v", err)
	}
	recovered, err := windowsRT.readManifest()
	if err != nil || recovered.Phase != manifestPhaseActive || !receiptManifestCovers(recovered, receiptEffects(policy)) {
		t.Fatalf("recovered manifest = %+v/%v, want complete active receipts", recovered, err)
	}
}

func TestResetFailsClosedWhileRuntimeStateIsActive(t *testing.T) {
	stateDir := t.TempDir()
	rtA, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatalf("New(workspace A) error = %v", err)
	}
	defer rtA.Close()
	rtB, err := New(sandbox.Config{CWD: t.TempDir(), StateDir: stateDir})
	if err != nil {
		t.Fatalf("New(workspace B) error = %v", err)
	}
	windowsA := rtA.(*runtime)

	if err := windowsA.Reset(context.Background()); !errors.Is(err, errSandboxStateBusy) {
		t.Fatalf("Reset(other Runtime active) error = %v, want %v", err, errSandboxStateBusy)
	}
	if err := rtB.Close(); err != nil {
		t.Fatalf("Close(workspace B) error = %v", err)
	}
	releaseUse, err := windowsA.stateCoordinator.beginUse(windowsA.registeredEnvRoot)
	if err != nil {
		t.Fatalf("beginUse() error = %v", err)
	}
	if err := windowsA.Reset(context.Background()); !errors.Is(err, errSandboxStateBusy) {
		releaseUse()
		t.Fatalf("Reset(command active) error = %v, want %v", err, errSandboxStateBusy)
	}
	releaseUse()
	if err := windowsA.Reset(context.Background()); err != nil {
		t.Fatalf("Reset(idle) error = %v", err)
	}
}

func TestFailedProcessTreeDrainKeepsResetAndGCBusyUntilRetrySucceeds(t *testing.T) {
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	windowsRT := rt.(*runtime)
	releaseUse, err := windowsRT.beginRuntimeUse()
	if err != nil {
		t.Fatalf("beginRuntimeUse() error = %v", err)
	}
	released := make(chan struct{})
	var drainAllowed atomic.Bool
	var drainAttempts atomic.Int32
	session := &windowsSession{
		releaseUse: func() {
			releaseUse()
			close(released)
		},
		drainTree: func(context.Context) error {
			drainAttempts.Add(1)
			if !drainAllowed.Load() {
				return errors.New("forced Job drain failure")
			}
			return nil
		},
	}
	go session.retryProcessTreeDrain()
	defer func() {
		drainAllowed.Store(true)
		select {
		case <-released:
		case <-time.After(5 * time.Second):
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for drainAttempts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if drainAttempts.Load() == 0 {
		t.Fatal("process-tree drain retry did not start")
	}
	if err := windowsRT.Reset(context.Background()); !errors.Is(err, errSandboxStateBusy) {
		t.Fatalf("Reset(drain unproven) error = %v, want %v", err, errSandboxStateBusy)
	}
	if err := windowsRT.retireAndRemoveSandboxEnv(context.Background(), windowsRT.registeredEnvRoot); !errors.Is(err, errSandboxStateBusy) {
		t.Fatalf("GC(drain unproven) error = %v, want %v", err, errSandboxStateBusy)
	}
	drainAllowed.Store(true)
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("process-tree drain retry did not release Runtime use")
	}
	if err := windowsRT.Reset(context.Background()); err != nil {
		t.Fatalf("Reset(after proven drain) error = %v", err)
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
	if containsPath(policy.WriteRoots, missingRoot) {
		t.Fatalf("WriteRoots = %#v, redundant descendant %q was not compacted", policy.WriteRoots, missingRoot)
	}
	if missing, err := acl.MissingFileDACLEntries(workspace, allowEntries(policy.sidForWriteRoot(workspace))...); err != nil || len(missing) != 0 {
		t.Fatalf("compacted workspace ACL entries = %#v/%v, want inherited coverage", missing, err)
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
	staleDenyEntries := denyEntries([]string{oldPolicy.sidForCoveredPath(readonly)})
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
	if len(missing) != 0 {
		t.Fatalf("foreground ensure retired ACL still present = %#v, want deferred preservation", missing)
	}
	if err := windowsRT.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if missing, err := acl.MissingFileDACLEntries(readonly, staleDenyEntries...); err != nil || len(missing) == 0 {
		t.Fatalf("stale readonly deny after idle refresh = %#v/%v, want exact retirement", missing, err)
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

	if err := windowsRT.Preflight(context.Background(), sandbox.PreflightOptions{}); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if _, err := os.Stat(windowsRT.manifestPath()); !os.IsNotExist(err) {
		t.Fatalf("manifest stat = %v, want no ACL receipt preparation", err)
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

	if err := windowsRT.Preflight(context.Background(), sandbox.PreflightOptions{AllowNonElevatedRepair: true}); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	manifest, err := windowsRT.readManifest()
	if err != nil || len(manifest.ManagedReceipts) == 0 {
		t.Fatalf("Preflight manifest = %+v/%v, want exact managed receipts", manifest, err)
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
	if !containsPath(plan.LegacyPaths, filepath.Dir(windowsRT.manifestPath())) {
		t.Fatalf("cleanup LegacyPaths = %#v, want current workspace manifest directory", plan.LegacyPaths)
	}
	if containsPath(plan.LegacyPaths, filepath.Join(stateDir, ".sandbox-bin")) || containsPath(plan.LegacyPaths, windowsRT.workspaceManifestBase()) {
		t.Fatalf("cleanup LegacyPaths = %#v, must not delete StateDir-wide or other-workspace state", plan.LegacyPaths)
	}
	if !containsPath(plan.LegacyPaths, filepath.Join(workspace, ".caelis-sandbox")) {
		t.Fatalf("cleanup LegacyPaths = %#v, want workspace sandbox env dir", plan.LegacyPaths)
	}
	if !containsPath(plan.LegacyPaths, windowsRT.sandboxEnvRoot(workspace)) {
		t.Fatalf("cleanup LegacyPaths = %#v, want only current workspace sandbox env", plan.LegacyPaths)
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
		WritableRoots:    []string{workspace, filepath.Join(workspace, ".agents", "skills")},
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
	asyncStatus, err := async.Status(ctx)
	if err != nil {
		t.Fatalf("async Status() error = %v", err)
	}
	if asyncStatus.SupportsInput {
		t.Fatalf("async status = %+v, want SupportsInput=false", asyncStatus)
	}
	if err := async.WriteInput(ctx, []byte("ignored\n")); err == nil {
		t.Fatal("async WriteInput() error = nil, want stdin rejection")
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

func containsManifestACE(entries []manifestACE, want manifestACE) bool {
	for _, entry := range entries {
		if entry == want {
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
