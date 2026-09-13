//go:build windows

package windows

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestWindowsRestrictedStartupDiagnostics(t *testing.T) {
	root := t.TempDir()
	rt, err := New(sandbox.Config{CWD: root, StateDir: t.TempDir(), HostAuthorityDir: t.TempDir(), ResourceLimits: &sandbox.ResourceLimits{WritePaths: []string{root}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	r := rt.(*runtime)
	policy, err := r.ensureForRequest(t.Context(), sandbox.CommandRequest{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, exe, command string
		args               []string
	}{
		{name: "cmd", exe: "cmd.exe", args: []string{"/d", "/c", "echo cmd-ready"}},
		{name: "raw-powershell", exe: "powershell.exe", args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "[Console]::WriteLine('raw-ready')"}},
		{name: "wrapped-console", command: "[Console]::WriteLine('wrapped-ready')"},
		{name: "wrapped-cmdlet", command: "Write-Output cmdlet-ready"},
		{name: "raw-cmdlet", exe: "powershell.exe", args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "[Console]::WriteLine('before-cmdlet'); Write-Output cmdlet-ready; [Console]::WriteLine('after-cmdlet')"}},
		{name: "qualified-cmdlet", exe: "powershell.exe", args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "[Console]::WriteLine($env:PSModulePath); Microsoft.PowerShell.Utility\\Write-Output qualified-ready"}},
		{name: "builtin-module-path", exe: "powershell.exe", args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$env:PSModulePath = $PSHOME + '\\Modules'; [Console]::WriteLine('before-builtin'); Write-Output builtin-ready"}},
		{name: "explicit-module-import", exe: "powershell.exe", args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "[Console]::WriteLine('before-import'); Import-Module ($PSHOME + '\\Modules\\Microsoft.PowerShell.Utility\\Microsoft.PowerShell.Utility.psd1') -Verbose; [Console]::WriteLine('after-import'); Write-Output import-ready"}},
	} {
		parent := t
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd, token, err := r.restrictedShellCommand(ctx, sandbox.CommandRequest{Command: tc.command, Dir: root}, false, policy)
			if err != nil {
				t.Fatal(err)
			}
			if tc.exe != "" {
				probe := exec.CommandContext(ctx, tc.exe, tc.args...)
				probe.Dir, probe.Env = cmd.Dir, cmd.Env
				cmd = probe
			}
			var stdout, stderr bytes.Buffer
			code, err, _ := runAtomicJobProcess(ctx, cmd, token, nil, &stdout, &stderr, func() {})
			parent.Logf("probe=%s code=%d error=%v stdout=%q stderr=%q", tc.name, code, err, stdout.String(), stderr.String())
			if err != nil || code != 0 {
				t.Fail()
			}
		})
	}
}
