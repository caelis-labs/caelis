//go:build windows

package windows

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestWindowsWorkspaceWriteSandboxE2E(t *testing.T) {
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E") != "1" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E=1 to run the Windows workspace-write sandbox e2e")
	}
	workspace := t.TempDir()
	stateRoot := t.TempDir()
	for _, dir := range []string{".git", ".codex", ".agents", "readonly"} {
		if err := os.MkdirAll(filepath.Join(workspace, dir), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	rt, err := New(sandbox.Config{
		CWD:              workspace,
		StateDir:         stateRoot,
		ReadOnlySubpaths: []string{"readonly"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if preparer, ok := rt.(sandbox.PreparableRuntime); ok {
		if err := preparer.Prepare(ctx); err != nil {
			t.Fatalf("Prepare() error = %v", err)
		}
	}

	run := func(command string) (sandbox.CommandResult, error) {
		return rt.Run(ctx, sandbox.CommandRequest{
			Command: command,
			Dir:     workspace,
			Timeout: 20 * time.Second,
			Constraints: sandbox.Constraints{
				Route:      sandbox.RouteSandbox,
				Backend:    sandbox.BackendWindows,
				Permission: sandbox.PermissionWorkspaceWrite,
				Network:    sandbox.NetworkEnabled,
			},
		})
	}

	result, err := run("Write-Output 'read-ok'; Get-ChildItem -LiteralPath $env:USERPROFILE -Name | Select-Object -First 1")
	if err != nil {
		t.Fatalf("read command error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "read-ok") {
		t.Fatalf("read stdout = %q", result.Stdout)
	}

	result, err = run("Set-Content -LiteralPath .\\workspace.txt -Value workspace-ok; Get-Content -LiteralPath .\\workspace.txt")
	if err != nil {
		t.Fatalf("workspace write error = %v; result=%+v", err, result)
	}
	if !strings.Contains(result.Stdout, "workspace-ok") {
		t.Fatalf("workspace stdout = %q", result.Stdout)
	}

	tempTarget := filepath.Join(os.TempDir(), "caelis-windows-sandbox-e2e-denied.txt")
	_ = os.Remove(tempTarget)
	result, err = run("$ErrorActionPreference='Stop'; Set-Content -LiteralPath '" + escapePowerShellSingleQuote(tempTarget) + "' -Value denied")
	if err == nil || result.ExitCode == 0 {
		t.Fatalf("outside temp write unexpectedly succeeded: result=%+v", result)
	}

	if home, homeErr := os.UserHomeDir(); homeErr == nil && strings.TrimSpace(home) != "" {
		homeTarget := filepath.Join(home, "caelis-windows-sandbox-e2e-denied.txt")
		_ = os.Remove(homeTarget)
		result, err = run("$ErrorActionPreference='Stop'; Set-Content -LiteralPath '" + escapePowerShellSingleQuote(homeTarget) + "' -Value denied")
		if err == nil || result.ExitCode == 0 {
			_ = os.Remove(homeTarget)
			t.Fatalf("outside home write unexpectedly succeeded: result=%+v", result)
		}
	}

	for _, denied := range []string{".git\\blocked.txt", ".codex\\blocked.txt", ".agents\\blocked.txt", "readonly\\blocked.txt"} {
		result, err = run("$ErrorActionPreference='Stop'; Set-Content -LiteralPath '" + denied + "' -Value denied")
		if err == nil || result.ExitCode == 0 {
			t.Fatalf("deny-write carveout %s unexpectedly succeeded: result=%+v", denied, result)
		}
	}

	result, err = run("cmd.exe /d /c echo cmd-ok")
	if err != nil || !strings.Contains(result.Stdout, "cmd-ok") {
		t.Fatalf("cmd smoke err=%v result=%+v", err, result)
	}

	result, err = run("Write-Output 'non-ascii: 中文输出'")
	if err != nil || !strings.Contains(result.Stdout, "中文输出") {
		t.Fatalf("non-ascii smoke err=%v result=%+v", err, result)
	}

	if git, err := exec.LookPath("git"); err == nil {
		result, err = run("& '" + escapePowerShellSingleQuote(git) + "' --version")
		if err != nil || !strings.Contains(strings.ToLower(result.Stdout), "git version") {
			t.Fatalf("git smoke err=%v result=%+v", err, result)
		}
	}

	if resetter, ok := rt.(sandbox.ResettableRuntime); ok {
		if err := resetter.Reset(ctx); err != nil {
			t.Fatalf("Reset() error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(stateRoot, ".sandbox", "workspace_write_manifest.json")); !os.IsNotExist(err) {
			t.Fatalf("manifest still exists or unexpected stat error: %v", err)
		}
	}
}

func escapePowerShellSingleQuote(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
