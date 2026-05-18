//go:build windows

package windows

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/runnerruntime"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestWindowsElevatedSandboxE2E(t *testing.T) {
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E") != "1" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E=1 to run the local-machine Windows Elevated sandbox e2e")
	}
	helper := strings.TrimSpace(os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E_HELPER"))
	if helper == "" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E_HELPER to a caelis.exe with internal helper dispatch")
	}
	if _, err := os.Stat(helper); err != nil {
		t.Fatalf("helper %q unavailable: %v", helper, err)
	}

	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	for _, name := range []string{".git", ".codex", ".agents"} {
		if err := os.MkdirAll(filepath.Join(workspace, name), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", name, err)
		}
	}
	readOnlyCarveout := "future-readonly"
	readOnlyCarveoutPath := filepath.Join(workspace, readOnlyCarveout)
	if _, err := os.Stat(readOnlyCarveoutPath); !os.IsNotExist(err) {
		t.Fatalf("read-only carveout exists before sandbox refresh: %v", err)
	}
	stateRoot := strings.TrimSpace(os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E_STATE"))
	if stateRoot == "" {
		stateRoot = filepath.Join(t.TempDir(), "state")
	}
	rt, err := New(sandbox.Config{
		CWD:              workspace,
		StateDir:         stateRoot,
		HelperPath:       helper,
		RequestedBackend: sandbox.BackendWindowsElevated,
		ReadOnlySubpaths: []string{readOnlyCarveout},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	preparer, ok := rt.(interface {
		Prepare(context.Context) error
	})
	if !ok {
		t.Fatalf("runtime does not expose explicit setup")
	}
	if err := preparer.Prepare(ctx); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	expectedWorkspace := workspace
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil && strings.TrimSpace(resolved) != "" {
		expectedWorkspace = resolved
	}
	started := time.Now()
	result, err := runE2EListingCommandWithTimings(ctx, t, rt, workspace)
	t.Logf("workspace listing command completed in %s", time.Since(started))
	if err != nil {
		t.Fatalf("workspace listing command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, expectedWorkspace) {
		t.Fatalf("workspace listing stdout = %q, want current location %q", result.Stdout, expectedWorkspace)
	}
	started = time.Now()
	result, err = runE2EListingCommandWithTimings(ctx, t, rt, workspace)
	t.Logf("second workspace listing command completed in %s", time.Since(started))
	if err != nil {
		t.Fatalf("second workspace listing command error = %v; result=%+v", err, result)
	}

	result, err = runE2ECommand(ctx, rt, workspace, `
Write-Output 'e2e-command-ok'
Set-Content -LiteralPath '.\workspace-write.txt' -Value 'workspace-write-ok'
Get-Content -LiteralPath '.\workspace-write.txt'
`, sandbox.NetworkDisabled, nil)
	if err != nil {
		t.Fatalf("workspace command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "e2e-command-ok") || !strings.Contains(result.Stdout, "workspace-write-ok") {
		t.Fatalf("workspace stdout = %q", result.Stdout)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "workspace-write.txt")); err != nil || !strings.Contains(string(data), "workspace-write-ok") {
		t.Fatalf("host read workspace file = %q/%v", data, err)
	}
	verifyControlDirsDenyWriteE2E(ctx, t, rt, workspace)
	verifyMissingReadOnlyCarveoutE2E(ctx, t, rt, workspace, readOnlyCarveout)
	verifyMissingHiddenCarveoutE2E(ctx, t, rt, workspace)

	result, err = runE2ECommand(ctx, rt, workspace, `Write-Output '中文输出正常'`, sandbox.NetworkDisabled, nil)
	if err != nil {
		t.Fatalf("unicode command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "中文输出正常") || strings.Contains(result.Stdout, "\ufffd") {
		t.Fatalf("unicode stdout = %q", result.Stdout)
	}

	for _, check := range devToolChecks() {
		if check.exe == "" {
			continue
		}
		escapedExe := strings.ReplaceAll(check.exe, "'", "''")
		outputFile := check.name + "-version.txt"
		result, err = runE2ECommand(ctx, rt, workspace, `
$out = & '`+escapedExe+`' `+check.args+`
@('tool=`+check.name+`', $out) | Set-Content -LiteralPath '.\`+outputFile+`'
Get-Content -LiteralPath '.\`+outputFile+`'
`, sandbox.NetworkDisabled, nil)
		if err != nil {
			t.Fatalf("%s command error = %v; result=%+v", check.name, err, result)
		}
		if !strings.Contains(result.Stdout, "tool="+check.name) || (check.want != "" && !strings.Contains(result.Stdout, check.want)) {
			t.Fatalf("%s stdout = %q", check.name, result.Stdout)
		}
		if data, err := os.ReadFile(filepath.Join(workspace, outputFile)); err != nil || !strings.Contains(string(data), "tool="+check.name) {
			t.Fatalf("host read %s version file = %q/%v", check.name, data, err)
		}
	}

	result, err = runE2ECommand(ctx, rt, workspace, `
Write-Output "net=$env:CAELIS_SANDBOX_NETWORK"
Write-Output "proxy=$env:HTTP_PROXY"
`, sandbox.NetworkDisabled, nil)
	if err != nil {
		t.Fatalf("offline network env command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "net=disabled") || !strings.Contains(result.Stdout, "proxy=http://127.0.0.1:9") {
		t.Fatalf("offline network stdout = %q", result.Stdout)
	}

	result, err = runE2ECommand(ctx, rt, workspace, `
Write-Output "net=$env:CAELIS_SANDBOX_NETWORK"
Write-Output "proxy=$env:HTTP_PROXY"
`, sandbox.NetworkEnabled, nil)
	if err != nil {
		t.Fatalf("online network env command error = %v; result=%+v", err, result)
	}
	if strings.Contains(result.Stdout, "net=disabled") || strings.Contains(result.Stdout, "proxy=http://127.0.0.1:9") {
		t.Fatalf("online network stdout = %q, want no offline proxy hardening", result.Stdout)
	}

	if endpoint := reachableExternalE2EEndpoint(t); endpoint != "" {
		host, port, err := net.SplitHostPort(endpoint)
		if err != nil {
			t.Fatalf("SplitHostPort(%q) error = %v", endpoint, err)
		}
		escapedHost := strings.ReplaceAll(host, "'", "''")
		result, err = runE2ECommand(ctx, rt, workspace, `
$client = [System.Net.Sockets.TcpClient]::new()
$client.Connect('`+escapedHost+`', `+port+`)
$client.Close()
Write-Output 'online-connected'
`, sandbox.NetworkEnabled, nil)
		if err != nil {
			t.Fatalf("online socket command error = %v; result=%+v", err, result)
		}
		if !strings.Contains(result.Stdout, "online-connected") {
			t.Fatalf("online socket stdout = %q", result.Stdout)
		}

		result, err = runE2ECommand(ctx, rt, workspace, `
$client = [System.Net.Sockets.TcpClient]::new()
try {
  $async = $client.BeginConnect('`+escapedHost+`', `+port+`, $null, $null)
  if (-not $async.AsyncWaitHandle.WaitOne(2000)) {
    $client.Close()
    Write-Output 'offline-blocked'
    exit 0
  }
  $client.EndConnect($async)
  $client.Close()
  Write-Error 'offline unexpectedly connected'
  exit 1
} catch {
  $client.Close()
  Write-Output 'offline-blocked'
}
`, sandbox.NetworkDisabled, nil)
		if err != nil {
			t.Fatalf("offline socket command error = %v; result=%+v", err, result)
		}
		if !strings.Contains(result.Stdout, "offline-blocked") {
			t.Fatalf("offline socket stdout = %q", result.Stdout)
		}
	}

	secretDir := filepath.Join(workspace, "secret")
	secretFile := filepath.Join(secretDir, "token.txt")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(secret) error = %v", err)
	}
	if err := os.WriteFile(secretFile, []byte("super-secret"), 0o600); err != nil {
		t.Fatalf("WriteFile(secret) error = %v", err)
	}
	hiddenRules := []sandbox.PathRule{{Path: secretDir, Access: sandbox.PathAccessHidden}}
	escapedSecret := strings.ReplaceAll(secretFile, "'", "''")
	result, err = runE2ECommand(ctx, rt, workspace, "$ErrorActionPreference = 'Stop'; Get-Content -LiteralPath '"+escapedSecret+"'", sandbox.NetworkDisabled, hiddenRules)
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("hidden path command unexpectedly succeeded: result=%+v", result)
	}

	session, err := rt.Start(ctx, sandbox.CommandRequest{
		Command: "$name = Read-Host '请输入你的名字'; Write-Host ('你好，' + $name + '！')",
		Dir:     workspace,
		Timeout: 30 * time.Second,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindowsElevated,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkDisabled,
		},
	})
	if err != nil {
		t.Fatalf("Start(Read-Host) error = %v", err)
	}
	status, err := session.Wait(ctx, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Wait(Read-Host running) error = %v", err)
	}
	if !status.Running {
		result, _ := session.Result(ctx)
		t.Fatalf("Read-Host session exited before TASK write: status=%+v result=%+v", status, result)
	}
	if err := session.WriteInput(ctx, []byte("世界\n")); err != nil {
		t.Fatalf("WriteInput(Read-Host) error = %v", err)
	}
	status, err = session.Wait(ctx, 30*time.Second)
	if err != nil {
		t.Fatalf("Wait(Read-Host complete) error = %v", err)
	}
	if status.Running || status.ExitCode != 0 {
		t.Fatalf("Read-Host status = %+v", status)
	}
	result, err = session.Result(ctx)
	if err != nil {
		t.Fatalf("Result(Read-Host) error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "你好，世界！") || strings.Contains(result.Stdout, "\ufffd") {
		t.Fatalf("Read-Host stdout = %q", result.Stdout)
	}

	session, err = rt.Start(ctx, sandbox.CommandRequest{
		Command: "Start-Sleep -Milliseconds 100; Write-Output 'async-e2e-ok'",
		Dir:     workspace,
		Timeout: 30 * time.Second,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindowsElevated,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkDisabled,
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	status, err = session.Wait(ctx, 30*time.Second)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if status.Running || status.ExitCode != 0 {
		t.Fatalf("async status = %+v", status)
	}
	result, err = session.Result(ctx)
	if err != nil {
		t.Fatalf("Result() error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "async-e2e-ok") {
		t.Fatalf("async stdout = %q", result.Stdout)
	}
	verifyCanceledRunTerminatesChildE2E(ctx, t, rt, workspace)
}

func verifyControlDirsDenyWriteE2E(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string) {
	t.Helper()
	for _, name := range []string{".git", ".codex", ".agents"} {
		hostPath := filepath.Join(workspace, name, "e2e-denied.txt")
		relPath := filepath.Join(".", name, "e2e-denied.txt")
		assertSandboxWriteDeniedE2E(ctx, t, rt, workspace, relPath, nil)
		assertHostPathMissingE2E(t, hostPath)
	}
}

func verifyMissingReadOnlyCarveoutE2E(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string, carveout string) {
	t.Helper()
	hostDir := filepath.Join(workspace, carveout)
	if info, err := os.Stat(hostDir); err != nil || !info.IsDir() {
		t.Fatalf("read-only carveout was not materialized by sandbox refresh: info=%v err=%v", info, err)
	}
	hostPath := filepath.Join(hostDir, "leak.txt")
	relPath := filepath.Join(".", carveout, "leak.txt")
	assertSandboxWriteDeniedE2E(ctx, t, rt, workspace, relPath, nil)
	assertHostPathMissingE2E(t, hostPath)
}

func verifyMissingHiddenCarveoutE2E(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string) {
	t.Helper()
	hiddenDir := filepath.Join(workspace, "future-hidden")
	if err := os.RemoveAll(hiddenDir); err != nil {
		t.Fatalf("RemoveAll(future-hidden) error = %v", err)
	}
	if _, err := os.Stat(hiddenDir); !os.IsNotExist(err) {
		t.Fatalf("future-hidden exists before hidden carveout refresh: %v", err)
	}
	hiddenRules := []sandbox.PathRule{{Path: hiddenDir, Access: sandbox.PathAccessHidden}}
	hostPath := filepath.Join(hiddenDir, "leak.txt")
	assertSandboxWriteDeniedE2E(ctx, t, rt, workspace, filepath.Join(".", "future-hidden", "leak.txt"), hiddenRules)
	if info, err := os.Stat(hiddenDir); err != nil || !info.IsDir() {
		t.Fatalf("hidden carveout was not materialized by sandbox refresh: info=%v err=%v", info, err)
	}
	assertHostPathMissingE2E(t, hostPath)
}

func verifyCanceledRunTerminatesChildE2E(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string) {
	t.Helper()
	startedPath := filepath.Join(workspace, "cancel-started.txt")
	leakPath := filepath.Join(workspace, "cancel-leak.txt")
	_ = os.Remove(startedPath)
	_ = os.Remove(leakPath)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct {
		result sandbox.CommandResult
		err    error
	}, 1)
	go func() {
		result, err := rt.Run(runCtx, sandbox.CommandRequest{
			Command: strings.TrimSpace(`
Set-Content -LiteralPath '.\cancel-started.txt' -Value 'started'
Start-Sleep -Seconds 6
Set-Content -LiteralPath '.\cancel-leak.txt' -Value 'leaked'
`),
			Dir:     workspace,
			Timeout: 30 * time.Second,
			Constraints: sandbox.Constraints{
				Route:      sandbox.RouteSandbox,
				Backend:    sandbox.BackendWindowsElevated,
				Permission: sandbox.PermissionWorkspaceWrite,
				Network:    sandbox.NetworkDisabled,
			},
		})
		done <- struct {
			result sandbox.CommandResult
			err    error
		}{result: result, err: err}
	}()

	waitForHostPathE2E(t, startedPath, 15*time.Second)
	cancel()
	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("canceled Run() error = nil; result=%+v", got.result)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("canceled Run() did not return after context cancellation")
	}
	time.Sleep(7 * time.Second)
	assertHostPathMissingE2E(t, leakPath)
}

func assertSandboxWriteDeniedE2E(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string, relPath string, rules []sandbox.PathRule) {
	t.Helper()
	command := "$ErrorActionPreference = 'Stop'; Set-Content -LiteralPath '" + escapePowerShellSingleQuote(relPath) + "' -Value 'blocked'"
	result, err := runE2ECommand(ctx, rt, workspace, command, sandbox.NetworkDisabled, rules)
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("sandbox write to %s was not denied: err=%v result=%+v", relPath, err, result)
	}
}

func waitForHostPathE2E(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("Stat(%s) error = %v", path, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func assertHostPathMissingE2E(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("host path %s exists or stat failed unexpectedly: %v", path, err)
	}
}

func escapePowerShellSingleQuote(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func runE2EListingCommandWithTimings(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string) (sandbox.CommandResult, error) {
	t.Helper()
	command := `Get-Location; Get-ChildItem -Force | Select-Object Name, Length, LastWriteTime, Mode`
	windowsRT, ok := rt.(*runtime)
	if !ok || windowsRT.runner == nil {
		return runE2ECommand(ctx, rt, workspace, command, sandbox.NetworkDisabled, nil)
	}
	req := runnerruntime.Request{
		Command: command,
		Dir:     workspace,
		Timeout: 30 * time.Second,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindowsElevated,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkDisabled,
		},
	}
	started := time.Now()
	if err := windowsRT.runner.requireSetupReady(); err != nil {
		t.Logf("workspace listing requireSetupReady failed in %s", time.Since(started))
		return sandbox.CommandResult{}, err
	}
	t.Logf("workspace listing requireSetupReady completed in %s", time.Since(started))
	started = time.Now()
	if err := windowsRT.runner.refreshRequestACLs(req); err != nil {
		t.Logf("workspace listing refreshRequestACLs failed in %s", time.Since(started))
		return sandbox.CommandResult{}, err
	}
	t.Logf("workspace listing refreshRequestACLs completed in %s", time.Since(started))
	started = time.Now()
	result, err := windowsRT.runner.client.Run(ctx, req)
	t.Logf("workspace listing runner execution completed in %s", time.Since(started))
	return result, err
}

func runE2ECommand(ctx context.Context, rt sandbox.Runtime, workspace string, command string, network sandbox.Network, rules []sandbox.PathRule) (sandbox.CommandResult, error) {
	return rt.Run(ctx, sandbox.CommandRequest{
		Command: strings.TrimSpace(command),
		Dir:     workspace,
		Timeout: 30 * time.Second,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindowsElevated,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    network,
			PathRules:  rules,
		},
	})
}

type devToolCheck struct {
	name string
	exe  string
	args string
	want string
}

func devToolChecks() []devToolCheck {
	systemRoot := firstNonEmptyE2E(os.Getenv("SystemRoot"), `C:\Windows`)
	return []devToolCheck{
		{name: "go", exe: firstExisting(
			filepath.Join(os.Getenv("ProgramFiles"), "Go", "bin", "go.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Go", "bin", "go.exe"),
		), args: "version", want: "go version"},
		{name: "git", exe: firstExisting(
			filepath.Join(os.Getenv("ProgramFiles"), "Git", "cmd", "git.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "git.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Git", "cmd", "git.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Git", "bin", "git.exe"),
		), args: "--version", want: "git version"},
		{name: "npm", exe: firstExisting(
			filepath.Join(os.Getenv("ProgramFiles"), "nodejs", "npm.cmd"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "nodejs", "npm.cmd"),
		), args: "--version"},
		{name: "powershell", exe: firstExisting(
			filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"),
		), args: `-NoLogo -NoProfile -NonInteractive -Command "Write-Output nested-powershell-ok"`, want: "nested-powershell-ok"},
	}
}

func firstExisting(paths ...string) string {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func firstNonEmptyE2E(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		return value
	}
	return ""
}

func reachableExternalE2EEndpoint(t *testing.T) string {
	t.Helper()
	for _, endpoint := range []string{
		net.JoinHostPort("1.1.1.1", "443"),
		net.JoinHostPort("8.8.8.8", "53"),
		net.JoinHostPort("223.5.5.5", "53"),
	} {
		conn, err := net.DialTimeout("tcp", endpoint, 3*time.Second)
		if err == nil {
			_ = conn.Close()
			return endpoint
		}
		t.Logf("external network e2e endpoint %s unavailable from host: %v", endpoint, err)
	}
	t.Log("skipping external socket network e2e: no probe endpoint is reachable from host")
	return ""
}
