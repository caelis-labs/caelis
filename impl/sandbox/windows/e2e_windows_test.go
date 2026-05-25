//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/runnerruntime"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/netpolicy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/runnertrace"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/winexec"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func enableRunnerTraceE2E(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	restoreEnabled := runnertrace.SetEnabled(true)
	restoreSink := runnertrace.SetSink(func(line string) {
		mu.Lock()
		lines = append(lines, strings.TrimRight(line, "\r\n"))
		mu.Unlock()
	})
	t.Cleanup(func() {
		restoreSink()
		restoreEnabled()
		mu.Lock()
		defer mu.Unlock()
		if len(lines) == 0 {
			return
		}
		t.Logf("windows runner trace:\n%s", strings.Join(lines, "\n"))
	})
}

func TestWindowsElevatedSandboxE2E(t *testing.T) {
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E") != "1" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E=1 to run the local-machine Windows Elevated sandbox e2e")
	}
	enableRunnerTraceE2E(t)
	helper := strings.TrimSpace(os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E_HELPER"))
	if helper == "" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E_HELPER to a caelis.exe with internal helper dispatch")
	}
	if _, err := os.Stat(helper); err != nil {
		t.Fatalf("helper %q unavailable: %v", helper, err)
	}

	workspace := strings.TrimSpace(os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E_WORKSPACE"))
	if workspace == "" {
		workspace = filepath.Join(t.TempDir(), "workspace")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	listingSentinel := "caelis-e2e-listing.txt"
	if err := os.WriteFile(filepath.Join(workspace, listingSentinel), []byte("listing-ok"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", listingSentinel, err)
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

	started := time.Now()
	result, err := runE2EListingCommandWithTimings(ctx, t, rt, workspace)
	t.Logf("workspace listing command completed in %s", time.Since(started))
	if err != nil {
		t.Fatalf("workspace listing command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, listingSentinel) {
		t.Fatalf("workspace listing stdout = %q stderr = %q, want sentinel %q from workspace", result.Stdout, result.Stderr, listingSentinel)
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
	verifyNetworkEnabledE2E(ctx, t, rt, workspace, stateRoot, readOnlyCarveout)
	verifyMissingReadOnlyCarveoutE2E(ctx, t, rt, workspace, readOnlyCarveout)
	verifyMissingHiddenCarveoutE2E(ctx, t, rt, workspace)

	linebreakNames := []string{"caelis-linebreak-a", "caelis-linebreak-b", "caelis-linebreak-c"}
	for _, name := range linebreakNames {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(name), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	result, err = runE2ECommand(ctx, rt, workspace, `Get-ChildItem -LiteralPath . -Name -Force | Where-Object { $_ -like 'caelis-linebreak-*' } | Sort-Object`, sandbox.NetworkDisabled, nil)
	if err != nil {
		t.Fatalf("Get-ChildItem -Name linebreak command error = %v; result=%+v", err, result)
	}
	gotLinebreakNames := nonEmptyE2EOutputLines(result.Stdout)
	if strings.Join(gotLinebreakNames, "|") != strings.Join(linebreakNames, "|") {
		t.Fatalf("Get-ChildItem -Name stdout lines = %#v, want %#v; raw stdout=%q stderr=%q", gotLinebreakNames, linebreakNames, result.Stdout, result.Stderr)
	}
	if strings.Contains(result.Stdout, strings.Join(linebreakNames, "")) {
		t.Fatalf("Get-ChildItem -Name stdout lost line breaks: %q", result.Stdout)
	}

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
	verifyGitAndGoCommandOutputE2E(ctx, t, rt, workspace)

	if endpoint := reachableExternalE2EEndpoint(t); endpoint != "" {
		host, port, err := net.SplitHostPort(endpoint)
		if err != nil {
			t.Fatalf("SplitHostPort(%q) error = %v", endpoint, err)
		}
		escapedHost := strings.ReplaceAll(host, "'", "''")
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

func TestWindowsElevatedSandboxSmokeE2E(t *testing.T) {
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_SMOKE_E2E") != "1" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_SMOKE_E2E=1 to run the local-machine Windows Elevated sandbox smoke e2e")
	}
	enableRunnerTraceE2E(t)
	helper := strings.TrimSpace(os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E_HELPER"))
	if helper == "" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E_HELPER to a caelis.exe with internal helper dispatch")
	}
	if _, err := os.Stat(helper); err != nil {
		t.Fatalf("helper %q unavailable: %v", helper, err)
	}
	workspace := strings.TrimSpace(os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E_WORKSPACE"))
	if workspace == "" {
		workspace = filepath.Join(t.TempDir(), "workspace")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	listingSentinel := "caelis-smoke-listing.txt"
	if err := os.WriteFile(filepath.Join(workspace, listingSentinel), []byte("listing-ok"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", listingSentinel, err)
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
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	preparer, ok := rt.(interface {
		Prepare(context.Context) error
	})
	if !ok {
		t.Fatalf("runtime does not expose explicit setup")
	}
	resetter, ok := rt.(interface {
		Reset(context.Context) error
	})
	if !ok {
		t.Fatalf("runtime does not expose reset")
	}
	var resetOnce sync.Once
	var resetErr error
	resetSandbox := func(ctx context.Context) error {
		resetOnce.Do(func() {
			resetErr = resetter.Reset(ctx)
		})
		return resetErr
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
		defer cancel()
		if err := resetSandbox(cleanupCtx); err != nil {
			t.Logf("cleanup Reset() error = %v", err)
		}
	})
	seedManagedFirewallRulesByInternalNameE2E(ctx, t)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := netpolicy.ClearContext(cleanupCtx); err != nil {
			t.Logf("cleanup managed firewall rules: %v", err)
		}
	})
	started := time.Now()
	if err := preparer.Prepare(ctx); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	t.Logf("smoke setup completed in %s", time.Since(started))

	started = time.Now()
	result, err := runE2EListingCommandWithTimings(ctx, t, rt, workspace)
	t.Logf("smoke listing completed in %s", time.Since(started))
	if err != nil {
		t.Fatalf("listing command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, listingSentinel) {
		t.Fatalf("listing stdout = %q stderr = %q, want sentinel %q from workspace", result.Stdout, result.Stderr, listingSentinel)
	}
	result, err = runE2ECommand(ctx, rt, workspace, "Write-Output 'smoke-ok'", sandbox.NetworkDisabled, nil)
	if err != nil {
		t.Fatalf("smoke command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "smoke-ok") {
		t.Fatalf("smoke stdout = %q", result.Stdout)
	}
	result, err = runE2ECommand(ctx, rt, workspace, "whoami", sandbox.NetworkDisabled, nil)
	if err != nil {
		t.Fatalf("whoami command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(strings.ToLower(result.Stdout), "caelissbxoff") {
		t.Fatalf("whoami stdout = %q, want offline sandbox user", result.Stdout)
	}
	result, err = runE2ECommand(ctx, rt, workspace, `$path = Join-Path $env:TEMP 'caelis-smoke-temp.txt'; Set-Content -LiteralPath $path -Value 'temp-ok' -Force; $value = Get-Content -LiteralPath $path -Raw; Write-Output $value.Trim(); [System.IO.File]::Delete($path); if (Test-Path -LiteralPath $path) { throw 'temp delete failed' }`, sandbox.NetworkDisabled, nil)
	if err != nil {
		t.Fatalf("sandbox temp write command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "temp-ok") {
		t.Fatalf("sandbox temp stdout = %q", result.Stdout)
	}
	result, err = runE2ECommand(ctx, rt, workspace, `
$temp = [Environment]::GetEnvironmentVariable('TEMP')
if ([string]::IsNullOrWhiteSpace($temp)) { throw 'TEMP missing' }
$path = Join-Path $temp 'caelis-cache-smoke.txt'
Set-Content -LiteralPath $path -Value 'temp' -Force
Remove-Item -LiteralPath $path -Force
$sandboxHome = [Environment]::GetEnvironmentVariable('CAELIS_SANDBOX_HOME')
$profile = [Environment]::GetEnvironmentVariable('USERPROFILE')
if (-not [string]::IsNullOrWhiteSpace($profile) -and -not [string]::IsNullOrWhiteSpace($sandboxHome) -and $profile.StartsWith($sandboxHome, [System.StringComparison]::OrdinalIgnoreCase)) {
  throw "USERPROFILE was redirected under sandbox home: $profile"
}
foreach ($name in @('GOCACHE', 'GOPATH', 'GOMODCACHE', 'npm_config_cache')) {
  $value = [Environment]::GetEnvironmentVariable($name)
  if (-not [string]::IsNullOrWhiteSpace($value) -and -not [string]::IsNullOrWhiteSpace($sandboxHome) -and $value.StartsWith($sandboxHome, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "$name was redirected under sandbox home: $value"
  }
}
Write-Output 'cache-env-ok'
`, sandbox.NetworkDisabled, nil)
	if err != nil {
		t.Fatalf("sandbox cache env write command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "cache-env-ok") {
		t.Fatalf("sandbox cache env stdout = %q", result.Stdout)
	}
	result, err = runE2ECommand(ctx, rt, workspace, "Write-Error 'raw-powershell-error'; exit 17", sandbox.NetworkDisabled, nil)
	if err == nil || result.ExitCode != 17 {
		t.Fatalf("raw PowerShell error command = err %v result %+v, want exit 17 failure", err, result)
	}
	if !strings.Contains(result.Stderr, "raw-powershell-error") {
		t.Fatalf("raw PowerShell stderr = %q, want original error output", result.Stderr)
	}
	verifyAsyncSessionSmokeE2E(ctx, t, rt, workspace)
	started = time.Now()
	if err := resetSandbox(ctx); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	t.Logf("smoke reset completed in %s", time.Since(started))
	assertManagedFirewallRulesAbsentE2E(t)
	assertLocalAccountMissingE2E(ctx, t, "user", setupOfflineUser(stateRoot))
	assertLocalAccountMissingE2E(ctx, t, "user", setupOnlineUser(stateRoot))
	assertLocalAccountMissingE2E(ctx, t, "localgroup", "CaelisSandboxUsers")
	assertSandboxUserProfilesAbsentE2E(t, setupOfflineUser(stateRoot), setupOnlineUser(stateRoot))
	assertSandboxStateDirsAbsentE2E(t, stateRoot)
}

func verifyAsyncSessionSmokeE2E(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string) {
	t.Helper()
	started := time.Now()
	session, err := rt.Start(ctx, sandbox.CommandRequest{
		Command: "Write-Output 'async-smoke-ok'",
		Dir:     workspace,
		Timeout: 10 * time.Second,
		Constraints: sandbox.Constraints{
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	})
	if err != nil {
		t.Fatalf("async smoke session start error = %v", err)
	}
	resultCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	result, err := session.Result(resultCtx)
	cancel()
	if err != nil {
		_ = session.Terminate(context.Background())
		t.Fatalf("async smoke session error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "async-smoke-ok") {
		t.Fatalf("async smoke stdout = %q", result.Stdout)
	}
	t.Logf("async NetworkEnabled session completed in %s", time.Since(started))
}

func seedManagedFirewallRulesByInternalNameE2E(ctx context.Context, t *testing.T) {
	t.Helper()
	script := strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$ProgressPreference = 'SilentlyContinue'",
		"# Seed inert stale rules by Caelis-owned names. They must be disabled",
		"# allow rules so this e2e cannot affect host network traffic.",
		"$rules = @(",
		"  @{ Name = 'CaelisSandbox-Offline-Block-NonLoopback'; Protocol = 'Any'; RemoteAddress = '192.0.2.0/24' },",
		"  @{ Name = 'CaelisSandbox-Offline-Block-Loopback-TCP'; Protocol = 'TCP'; RemoteAddress = '198.51.100.0/24' },",
		"  @{ Name = 'CaelisSandbox-Offline-Block-Loopback-UDP'; Protocol = 'UDP'; RemoteAddress = '203.0.113.0/24' }",
		")",
		"foreach ($rule in $rules) {",
		"  Get-NetFirewallRule -PolicyStore PersistentStore -Name $rule.Name -ErrorAction SilentlyContinue | Remove-NetFirewallRule -ErrorAction SilentlyContinue",
		"  New-NetFirewallRule -Name $rule.Name -DisplayName ('Caelis e2e inert stale rule ' + $rule.Name) -Group 'Caelis Sandbox' -Direction Outbound -Action Allow -Profile Any -PolicyStore PersistentStore -Enabled False -Protocol $rule.Protocol -RemoteAddress $rule.RemoteAddress | Out-Null",
		"}",
	}, "\n")
	result, err := winexec.Run(ctx, "powershell.exe", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}, winexec.Options{
		Timeout:        60 * time.Second,
		TraceComponent: "windows-e2e",
		TraceName:      "seed_firewall_rules",
		DisplayArgs:    []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "<script>"},
	})
	if err != nil {
		t.Fatalf("seed managed firewall rules by internal name: %v: %s", err, strings.TrimSpace(string(result.CombinedOutput())))
	}
}

func assertManagedFirewallRulesAbsentE2E(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	present, err := netpolicy.ManagedRulesPresentContext(ctx)
	if err != nil {
		t.Fatalf("ManagedRulesPresentContext() error = %v", err)
	}
	if len(present) != 0 {
		t.Fatalf("managed firewall rules remain after reset: %v", present)
	}
}

func assertLocalAccountMissingE2E(ctx context.Context, t *testing.T, kind string, name string) {
	t.Helper()
	result, err := winexec.Run(ctx, "net.exe", []string{kind, name}, winexec.Options{
		Timeout:        10 * time.Second,
		TraceComponent: "windows-e2e",
		TraceName:      "account_absent",
	})
	if err == nil && result.ExitCode == 0 {
		t.Fatalf("net %s %s unexpectedly succeeded after reset", kind, name)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatalf("net %s %s did not return: %v", kind, name, err)
	}
}

func assertSandboxUserProfilesAbsentE2E(t *testing.T, usernames ...string) {
	t.Helper()
	systemDrive := strings.TrimRight(strings.TrimSpace(os.Getenv("SystemDrive")), `\/`)
	if systemDrive == "" {
		systemDrive = `C:`
	}
	usersRoot := filepath.Join(systemDrive+`\`, "Users")
	for _, username := range usernames {
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(usersRoot, username+"*"))
		if err != nil {
			t.Fatalf("Glob sandbox user profiles for %s: %v", username, err)
		}
		for _, match := range matches {
			name := filepath.Base(match)
			if strings.EqualFold(name, username) || strings.HasPrefix(strings.ToLower(name), strings.ToLower(username)+".") {
				t.Fatalf("sandbox user profile %s still exists after reset", match)
			}
		}
	}
}

func assertSandboxStateDirsAbsentE2E(t *testing.T, stateRoot string) {
	t.Helper()
	for _, dir := range []string{
		filepath.Join(stateRoot, ".sandbox"),
		filepath.Join(stateRoot, ".sandbox-bin"),
		filepath.Join(stateRoot, ".sandbox-secrets"),
	} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("sandbox state directory %s still exists or stat failed unexpectedly: %v", dir, err)
		}
	}
}

func verifyControlDirsDenyWriteE2E(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string) {
	t.Helper()
	for _, name := range []string{".git", ".codex", ".agents"} {
		hostPath := filepath.Join(workspace, name, "e2e-denied.txt")
		relPath := filepath.Join(".", name, "e2e-denied.txt")
		assertSandboxWriteDeniedForNetworkE2E(ctx, t, rt, workspace, relPath, sandbox.NetworkDisabled, nil)
		assertHostPathMissingE2E(t, hostPath)
	}
}

func verifyNetworkEnabledE2E(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string, stateRoot string, readOnlyCarveout string) {
	t.Helper()
	result, err := runE2ECommand(ctx, rt, workspace, "whoami", sandbox.NetworkEnabled, nil)
	if err != nil {
		t.Fatalf("network-enabled whoami command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(strings.ToLower(result.Stdout), strings.ToLower(setupOnlineUser(stateRoot))) {
		t.Fatalf("network-enabled whoami stdout = %q, want online sandbox user %q", result.Stdout, setupOnlineUser(stateRoot))
	}

	result, err = runE2ECommand(ctx, rt, workspace, `
$profile = [Environment]::GetEnvironmentVariable('USERPROFILE')
if ([string]::IsNullOrWhiteSpace($profile)) { throw 'USERPROFILE missing' }
if ($profile -notmatch '\\Users\\CaelisSbxOn') { throw "unexpected USERPROFILE=$profile" }
Set-Content -LiteralPath '.\online-workspace-write.txt' -Value 'online-workspace-write-ok'
Get-Content -LiteralPath '.\online-workspace-write.txt'
`, sandbox.NetworkEnabled, nil)
	if err != nil {
		t.Fatalf("network-enabled workspace command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "online-workspace-write-ok") {
		t.Fatalf("network-enabled workspace stdout = %q", result.Stdout)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "online-workspace-write.txt")); err != nil || !strings.Contains(string(data), "online-workspace-write-ok") {
		t.Fatalf("host read online workspace file = %q/%v", data, err)
	}

	result, err = runE2ECommand(ctx, rt, workspace, `
$ErrorActionPreference = 'Stop'
$response = Invoke-WebRequest -UseBasicParsing -Method Head -Uri 'https://example.com/' -TimeoutSec 20
Write-Output ('https-status=' + [int]$response.StatusCode)
`, sandbox.NetworkEnabled, nil)
	if err != nil {
		t.Fatalf("network-enabled HTTPS command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "https-status=200") {
		t.Fatalf("network-enabled HTTPS stdout = %q", result.Stdout)
	}

	for _, name := range []string{".git", ".codex", ".agents"} {
		hostPath := filepath.Join(workspace, name, "e2e-online-denied.txt")
		assertSandboxWriteDeniedForNetworkE2E(ctx, t, rt, workspace, filepath.Join(".", name, "e2e-online-denied.txt"), sandbox.NetworkEnabled, nil)
		assertHostPathMissingE2E(t, hostPath)
	}
	hostPath := filepath.Join(workspace, readOnlyCarveout, "e2e-online-readonly-denied.txt")
	assertSandboxWriteDeniedForNetworkE2E(ctx, t, rt, workspace, filepath.Join(".", readOnlyCarveout, "e2e-online-readonly-denied.txt"), sandbox.NetworkEnabled, nil)
	assertHostPathMissingE2E(t, hostPath)
}

func verifyMissingReadOnlyCarveoutE2E(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string, carveout string) {
	t.Helper()
	hostDir := filepath.Join(workspace, carveout)
	if info, err := os.Stat(hostDir); err != nil || !info.IsDir() {
		t.Fatalf("read-only carveout was not materialized by sandbox refresh: info=%v err=%v", info, err)
	}
	hostPath := filepath.Join(hostDir, "leak.txt")
	relPath := filepath.Join(".", carveout, "leak.txt")
	assertSandboxWriteDeniedForNetworkE2E(ctx, t, rt, workspace, relPath, sandbox.NetworkDisabled, nil)
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
	assertSandboxWriteDeniedForNetworkE2E(ctx, t, rt, workspace, filepath.Join(".", "future-hidden", "leak.txt"), sandbox.NetworkDisabled, hiddenRules)
	assertSandboxWriteDeniedForNetworkE2E(ctx, t, rt, workspace, filepath.Join(".", "future-hidden", "online-leak.txt"), sandbox.NetworkEnabled, hiddenRules)
	if info, err := os.Stat(hiddenDir); err != nil || !info.IsDir() {
		t.Fatalf("hidden carveout was not materialized by sandbox refresh: info=%v err=%v", info, err)
	}
	assertHostPathMissingE2E(t, hostPath)
	assertHostPathMissingE2E(t, filepath.Join(hiddenDir, "online-leak.txt"))
}

func verifyGitAndGoCommandOutputE2E(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Logf("skipping git/go output e2e: host git is unavailable: %v", err)
		return
	}
	repo := prepareGitGoRepoE2E(t, workspace, gitPath)
	for _, tc := range []struct {
		name    string
		command string
		want    []string
	}{
		{name: "git status", command: "git status", want: []string{"On branch", "working tree clean"}},
		{name: "git log", command: "git --no-pager log --oneline -1", want: []string{"initial e2e"}},
		{name: "git show", command: "git --no-pager show HEAD:calc.go", want: []string{"func Add"}},
	} {
		result, runErr := runE2ECommandInDir(ctx, rt, repo, tc.command, sandbox.NetworkDisabled, nil)
		requireE2ECommandOutput(t, "sync "+tc.name, result, runErr, tc.want...)
		result, runErr = runE2EAsyncCommand(ctx, t, rt, repo, tc.command, sandbox.NetworkDisabled)
		requireE2ECommandOutput(t, "async "+tc.name, result, runErr, tc.want...)
	}

	if _, err := exec.LookPath("go"); err != nil {
		t.Logf("skipping go output e2e: host go is unavailable: %v", err)
		return
	}
	for _, tc := range []struct {
		name    string
		command string
		want    []string
	}{
		{name: "go test", command: "go test ./...", want: []string{"ok", "example.com/caelis-e2e"}},
	} {
		result, runErr := runE2ECommandInDir(ctx, rt, repo, tc.command, sandbox.NetworkDisabled, nil)
		requireE2ECommandOutput(t, "sync "+tc.name, result, runErr, tc.want...)
		result, runErr = runE2EAsyncCommand(ctx, t, rt, repo, tc.command, sandbox.NetworkDisabled)
		requireE2ECommandOutput(t, "async "+tc.name, result, runErr, tc.want...)
	}
}

func prepareGitGoRepoE2E(t *testing.T, workspace string, gitPath string) string {
	t.Helper()
	repo := filepath.Join(workspace, "git-go-output-repo")
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("RemoveAll(%s) error = %v", repo, err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", repo, err)
	}
	files := map[string]string{
		"go.mod":       "module example.com/caelis-e2e\n\ngo 1.21\n",
		"calc.go":      "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
		"calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"bad add\")\n\t}\n}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	runHostGitE2E(t, gitPath, repo, "init")
	runHostGitE2E(t, gitPath, repo, "config", "user.name", "Caelis E2E")
	runHostGitE2E(t, gitPath, repo, "config", "user.email", "caelis-e2e@example.invalid")
	runHostGitE2E(t, gitPath, repo, "add", ".")
	runHostGitE2E(t, gitPath, repo, "-c", "commit.gpgsign=false", "commit", "-m", "initial e2e")
	return repo
}

func runHostGitE2E(t *testing.T, gitPath string, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("host git %s error = %v output=%q", strings.Join(args, " "), err, string(output))
	}
}

func runE2EAsyncCommand(ctx context.Context, t *testing.T, rt sandbox.Runtime, dir string, command string, network sandbox.Network) (sandbox.CommandResult, error) {
	t.Helper()
	session, err := rt.Start(ctx, sandbox.CommandRequest{
		Command: strings.TrimSpace(command),
		Dir:     dir,
		Timeout: 45 * time.Second,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindowsElevated,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    network,
		},
	})
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	status, err := session.Wait(ctx, 45*time.Second)
	if err != nil {
		_ = session.Terminate(context.Background())
		return sandbox.CommandResult{}, fmt.Errorf("wait: %w", err)
	}
	if status.Running {
		stdout, stderr, _, _, _ := session.ReadOutput(context.Background(), 0, 0)
		_ = session.Terminate(context.Background())
		return sandbox.CommandResult{
			Stdout:   string(stdout),
			Stderr:   string(stderr),
			ExitCode: status.ExitCode,
		}, fmt.Errorf("async command still running after wait: status=%+v stdout_bytes=%d stderr_bytes=%d", status, len(stdout), len(stderr))
	}
	resultCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := session.Result(resultCtx)
	if err != nil {
		return result, fmt.Errorf("result after status=%+v: %w", status, err)
	}
	return result, nil
}

func requireE2ECommandOutput(t *testing.T, label string, result sandbox.CommandResult, err error, want ...string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s error = %v; %s", label, err, e2eCommandDiagnostics(result))
	}
	for _, one := range want {
		if !strings.Contains(result.Stdout, one) && !strings.Contains(result.Stderr, one) {
			t.Fatalf("%s missing %q; %s", label, one, e2eCommandDiagnostics(result))
		}
	}
	if strings.TrimSpace(result.Stdout) == "" && strings.TrimSpace(result.Stderr) == "" {
		t.Fatalf("%s produced no output; %s", label, e2eCommandDiagnostics(result))
	}
}

func e2eCommandDiagnostics(result sandbox.CommandResult) string {
	return fmt.Sprintf("exit=%d stdout_bytes=%d stderr_bytes=%d stdout=%q stderr=%q", result.ExitCode, len(result.Stdout), len(result.Stderr), result.Stdout, result.Stderr)
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

func assertSandboxWriteDeniedForNetworkE2E(ctx context.Context, t *testing.T, rt sandbox.Runtime, workspace string, relPath string, network sandbox.Network, rules []sandbox.PathRule) {
	t.Helper()
	command := "$ErrorActionPreference = 'Stop'; Set-Content -LiteralPath '" + escapePowerShellSingleQuote(relPath) + "' -Value 'blocked'"
	result, err := runE2ECommand(ctx, rt, workspace, command, network, rules)
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

func nonEmptyE2EOutputLines(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
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
	if err := windowsRT.runner.refreshRequestACLs(ctx, req); err != nil {
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
	return runE2ECommandInDir(ctx, rt, workspace, command, network, rules)
}

func runE2ECommandInDir(ctx context.Context, rt sandbox.Runtime, dir string, command string, network sandbox.Network, rules []sandbox.PathRule) (sandbox.CommandResult, error) {
	return rt.Run(ctx, sandbox.CommandRequest{
		Command: strings.TrimSpace(command),
		Dir:     dir,
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
