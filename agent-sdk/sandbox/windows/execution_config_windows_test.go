//go:build windows

package windows

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestSandboxEnvironmentPythonPathExplicitAuthority(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sandbox-env")
	base := []string{"PYTHONPATH=host-python"}
	for _, tc := range []struct {
		name      string
		execution *sandbox.ExecutionConfig
		req       sandbox.CommandRequest
		want      string
		present   bool
	}{
		{name: "default helper", want: prependEnvPath(sandboxPythonSiteDir(root), "host-python"), present: true},
		{name: "config replacement", execution: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{Set: map[string]string{"PYTHONPATH": "config-python"}}}, want: "config-python", present: true},
		{name: "config unset", execution: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{Unset: []string{"pythonpath"}}}},
		{name: "config empty", execution: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{Set: map[string]string{"PYTHONPATH": ""}}}, want: "", present: true},
		{name: "command replacement", req: sandbox.CommandRequest{Env: map[string]string{"PythonPath": "command-python"}}, want: "command-python", present: true},
		{name: "command unset", req: sandbox.CommandRequest{UnsetEnv: []string{"pythonpath"}}},
		{name: "command empty", req: sandbox.CommandRequest{Env: map[string]string{"PYTHONPATH": ""}}, want: "", present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := sandboxEnvironment(workspacePolicy{SandboxEnvRoot: root}, sandbox.Config{BaseEnv: base, Execution: tc.execution}, tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if got, present := envValue(env, "PYTHONPATH"); got != tc.want || present != tc.present {
				t.Fatalf("PYTHONPATH = %q/%t, want %q/%t", got, present, tc.want, tc.present)
			}
		})
	}
}

func TestRestrictedTokenExecutionConfigE2E(t *testing.T) {
	requireRestrictedExecutionConfigE2E(t)
	workspace := t.TempDir()
	child := filepath.Join(workspace, "child")
	temp := filepath.Join(workspace, "tmp")
	for _, dir := range []string{child, temp} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(child, "cwd-marker"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := append(os.Environ(),
		"CAELIS_EXEC_E2E_BASE=base space 雪",
		"CAELIS_EXEC_E2E_CONFIG_UNSET=base",
		"CAELIS_EXEC_E2E_COMMAND_UNSET=base",
	)
	inherit := false
	for _, tc := range []struct {
		name      string
		execution *sandbox.ExecutionConfig
		req       sandbox.CommandRequest
		want      map[string]string
		absent    []string
	}{
		{
			name: "default",
			want: map[string]string{"CAELIS_EXEC_E2E_BASE": "base space 雪", "CAELIS_EXEC_E2E_CONFIG_UNSET": "base"},
		},
		{
			name: "configured",
			execution: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{
				Set:   map[string]string{"CAELIS_EXEC_E2E_SET": "config 空 格", "CAELIS_EXEC_E2E_ORDER": "config"},
				Unset: []string{"caelis_exec_e2e_config_unset"},
			}},
			req: sandbox.CommandRequest{
				UnsetEnv: []string{"caelis_exec_e2e_order", "CAELIS_EXEC_E2E_COMMAND_UNSET"},
				Env:      map[string]string{"CaElIs_ExEc_E2E_OrDeR": "request 值", "CAELIS_EXEC_E2E_EMPTY": ""},
			},
			want: map[string]string{
				"CAELIS_EXEC_E2E_BASE": "base space 雪", "CAELIS_EXEC_E2E_SET": "config 空 格",
				"CAELIS_EXEC_E2E_ORDER": "request 值", "CAELIS_EXEC_E2E_EMPTY": "",
			},
			absent: []string{"CAELIS_EXEC_E2E_CONFIG_UNSET", "CAELIS_EXEC_E2E_COMMAND_UNSET"},
		},
		{
			name: "inherit false",
			execution: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{
				Inherit: &inherit,
				// PowerShell needs SystemRoot and writable temporary storage even
				// when the caller excludes the rest of the host environment.
				Set: map[string]string{
					"SystemRoot": os.Getenv("SystemRoot"), "TEMP": temp, "TMP": temp,
					"CAELIS_EXEC_E2E_SET": "explicit 空 格", "CAELIS_EXEC_E2E_ORDER": "config",
				},
			}},
			req: sandbox.CommandRequest{
				UnsetEnv: []string{"CAELIS_EXEC_E2E_ORDER"},
				Env:      map[string]string{"caelis_exec_e2e_order": "request 值", "CAELIS_EXEC_E2E_EMPTY": ""},
			},
			want: map[string]string{
				"SystemRoot": os.Getenv("SystemRoot"), "TEMP": temp, "TMP": temp,
				"CAELIS_EXEC_E2E_SET": "explicit 空 格", "CAELIS_EXEC_E2E_ORDER": "request 值", "CAELIS_EXEC_E2E_EMPTY": "",
			},
			absent: []string{"CAELIS_EXEC_E2E_BASE", "CAELIS_EXEC_E2E_CONFIG_UNSET", "CAELIS_SANDBOX_TEMP"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, err := New(sandbox.Config{
				CWD: workspace, StateDir: t.TempDir(), WritableRoots: []string{workspace},
				BaseEnv: base, Execution: tc.execution,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer closeRestrictedExecutionConfigRuntime(t, rt)
			for _, route := range []string{"run", "start", "conpty"} {
				t.Run(route, func(t *testing.T) {
					marker := "caelis-execution-config-" + route
					req := sandbox.CloneRequest(tc.req)
					req.Dir = "child"
					req.Command = restrictedEnvironmentCheckCommand(tc.want, tc.absent, marker)
					result, err := runRestrictedExecutionConfigCommand(t, rt, route, req)
					if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, marker) {
						t.Fatalf("restricted %s command: err=%v result=%+v", route, err, result)
					}
				})
			}
			if tc.name != "configured" {
				return
			}
			for _, route := range []string{"run", "start", "conpty"} {
				t.Run(route+" nonzero exit", func(t *testing.T) {
					req := sandbox.CloneRequest(tc.req)
					req.Dir = "child"
					req.Command = "Write-Output 'caelis-execution-nonzero'; exit 23"
					result, err := runRestrictedExecutionConfigCommand(t, rt, route, req)
					if err == nil || result.ExitCode != 23 || !strings.Contains(result.Stdout, "caelis-execution-nonzero") {
						t.Fatalf("restricted %s nonzero exit: err=%v result=%+v", route, err, result)
					}
				})
			}
		})
	}
}

func TestRestrictedTokenPythonEnvironmentE2E(t *testing.T) {
	requireRestrictedExecutionConfigE2E(t)
	python, err := exec.LookPath("python")
	if err != nil {
		t.Fatalf("Python 3.13+ is required for the Windows restricted-token helper check: %v", err)
	}
	version, err := exec.Command(python, "-c", "import sys; print(sys.version); sys.exit(sys.version_info < (3, 13))").CombinedOutput()
	if err != nil {
		t.Fatalf("Python 3.13+ is required: version=%q err=%v", version, err)
	}
	workspace := t.TempDir()
	pythonCommand := "& '" + strings.ReplaceAll(python, "'", "''") + "' '"
	defaultScript := `import tempfile
from pathlib import Path
assert tempfile.mkdtemp.__module__ == "sitecustomize"
directory = tempfile.mkdtemp(prefix="caelis-")
probe = Path(directory) / "ok"
probe.write_text("ok", encoding="utf-8")
print("caelis-python-default:" + probe.read_text(encoding="utf-8"))
`
	replacementScript := `import os
import tempfile
assert tempfile.mkdtemp.__module__ == "tempfile"
assert os.environ.get("PYTHONPATH") == "explicit-python"
print("caelis-python-replacement")
`
	for _, tc := range []struct {
		name      string
		execution *sandbox.ExecutionConfig
		req       sandbox.CommandRequest
		script    string
		marker    string
	}{
		{name: "default helper", script: defaultScript, marker: "caelis-python-default:ok"},
		{name: "config replacement", execution: &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{Set: map[string]string{"PYTHONPATH": "explicit-python"}}}, script: replacementScript, marker: "caelis-python-replacement"},
		{name: "command replacement", req: sandbox.CommandRequest{Env: map[string]string{"PYTHONPATH": "explicit-python"}}, script: replacementScript, marker: "caelis-python-replacement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scriptPath := filepath.Join(workspace, strings.ReplaceAll(tc.name, " ", "-")+".py")
			if err := os.WriteFile(scriptPath, []byte(tc.script), 0o600); err != nil {
				t.Fatal(err)
			}
			rt, err := New(sandbox.Config{CWD: workspace, StateDir: t.TempDir(), WritableRoots: []string{workspace}, Execution: tc.execution})
			if err != nil {
				t.Fatal(err)
			}
			defer closeRestrictedExecutionConfigRuntime(t, rt)
			req := sandbox.CloneRequest(tc.req)
			req.Command = pythonCommand + strings.ReplaceAll(scriptPath, "'", "''") + "'"
			result, err := runRestrictedExecutionConfigCommand(t, rt, "run", req)
			if err != nil || result.ExitCode != 0 || !strings.Contains(result.Stdout, tc.marker) {
				t.Fatalf("restricted Python %s: err=%v result=%+v", tc.name, err, result)
			}
		})
	}
}

func requireRestrictedExecutionConfigE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E") != "1" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E=1 for native restricted-token execution configuration tests")
	}
}

func restrictedEnvironmentCheckCommand(want map[string]string, absent []string, marker string) string {
	keys := make([]string, 0, len(want))
	for key := range want {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	checks := []string{"$ErrorActionPreference='Stop'"}
	// .NET Framework's single-value getter can return null for a present empty
	// variable. Check presence separately, then compare its string value.
	for _, key := range keys {
		checks = append(checks, fmt.Sprintf("if (-not (@([Environment]::GetEnvironmentVariables().Keys) -contains '%s') -or [string][Environment]::GetEnvironmentVariable('%s') -cne '%s') { [Console]::Error.WriteLine('environment mismatch: %s'); exit 41 }", key, key, strings.ReplaceAll(want[key], "'", "''"), key))
	}
	for _, key := range absent {
		checks = append(checks, fmt.Sprintf("if (@([Environment]::GetEnvironmentVariables().Keys) -contains '%s') { exit 42 }", key))
	}
	checks = append(checks, "if (-not (Test-Path -LiteralPath '.\\cwd-marker')) { exit 43 }", "Write-Output '"+marker+"'")
	return strings.Join(checks, "; ")
}

// Background manifest refresh may outlive a command and Close. Wait before
// t.TempDir removes the fixture's state directory, as in the workspace E2E.
func closeRestrictedExecutionConfigRuntime(t *testing.T, rt sandbox.Runtime) {
	t.Helper()
	defer rt.Close()
	windowsRT := rt.(*runtime)
	deadline := time.Now().Add(5 * time.Second)
	for {
		running, _, _, _, _ := windowsRT.refreshSnapshot()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Error("background refresh remained active before fixture cleanup")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runRestrictedExecutionConfigCommand(t *testing.T, rt sandbox.Runtime, route string, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	req.Timeout = 45 * time.Second
	req.Constraints = sandbox.Constraints{
		Route: sandbox.RouteSandbox, Backend: sandbox.BackendWindows,
		Permission: sandbox.PermissionWorkspaceWrite, Network: sandbox.NetworkEnabled,
	}
	var result sandbox.CommandResult
	var err error
	if route == "run" {
		result, err = rt.Run(ctx, req)
	} else {
		req.TTY = route == "conpty"
		session, startErr := rt.Start(ctx, req)
		if startErr != nil {
			return sandbox.CommandResult{}, startErr
		}
		status, waitErr := session.Wait(ctx, 50*time.Second)
		if waitErr != nil || status.Running {
			t.Fatalf("restricted %s Wait: status=%+v err=%v", route, status, waitErr)
		}
		result, err = session.Result(ctx)
	}
	if result.Route != sandbox.RouteSandbox || result.Backend != sandbox.BackendWindows {
		t.Fatalf("restricted %s route/backend = %q/%q, want sandbox/windows (err=%v)", route, result.Route, result.Backend, err)
	}
	return result, err
}
