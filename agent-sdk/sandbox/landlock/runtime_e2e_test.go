//go:build linux

package landlock

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
	"golang.org/x/sys/unix"
)

func TestLandlockFilesystemPolicySmoke(t *testing.T) {
	if os.Getenv("CAELIS_LINUX_SANDBOX_SMOKE_E2E") != "1" {
		t.Skip("set CAELIS_LINUX_SANDBOX_SMOKE_E2E=1 to run native Landlock filesystem tests")
	}
	abi, err := landlockABI()
	if err != nil {
		t.Fatalf("landlock unavailable on this kernel/runtime: %v", err)
	}
	t.Logf("Landlock ABI %d, ruleset attribute size %d", abi, unsafe.Sizeof(unix.LandlockRulesetAttr{}))
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name       string
		permission policy.Type
		want       string
	}{
		{name: "read-only", permission: policy.TypeReadOnly, want: "original"},
		{name: "workspace-write", permission: policy.TypeWorkspaceWrite, want: "write\ncreate\nmkdir\nremove\nchanged"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			for _, name := range []string{"existing", "removed"} {
				if err := os.WriteFile(filepath.Join(workspace, name), []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			command := `if printf changed > existing; then printf 'write\n'; fi
if printf created > created; then printf 'create\n'; fi
if mkdir created-dir; then printf 'mkdir\n'; fi
if rm removed; then printf 'remove\n'; fi
cat existing`
			args, err := buildLandlockHelperArgs(policy.Policy{
				Type: tt.permission, NetworkAccess: true, WritableRoots: []string{workspace},
			}, workspace, workspace, command, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			runner := &landlockRunner{execCommand: exec.CommandContext}
			cmd, payload, err := runner.helperCommand(ctx, helper, args, []string{"PATH=/usr/bin:/bin"})
			if err != nil {
				t.Fatal(err)
			}
			defer payload.Close()
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("Landlock helper failed: %v; stderr=%s", err, &stderr)
			}
			if string(output) != tt.want {
				t.Fatalf("filesystem operations = %q, want %q", output, tt.want)
			}
		})
	}
}

func TestSandboxedCommandTTYSmoke(t *testing.T) {
	if os.Getenv("CAELIS_LINUX_SANDBOX_SMOKE_E2E") != "1" {
		t.Skip("set CAELIS_LINUX_SANDBOX_SMOKE_E2E=1 to run the Linux landlock smoke test")
	}
	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace})
	if err != nil {
		if strings.Contains(err.Error(), "landlock sandbox unavailable") {
			t.Skipf("landlock unavailable on this kernel/runtime: %v", err)
		}
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	constraints := sandbox.Constraints{
		Route: sandbox.RouteSandbox, Backend: sandbox.BackendLandlock,
		Permission: sandbox.PermissionWorkspaceWrite, Network: sandbox.NetworkDisabled,
	}

	runResult, err := rt.Run(ctx, sandbox.CommandRequest{
		Command: "printf run-ok", Dir: workspace, Constraints: constraints,
	})
	if err != nil || runResult.ExitCode != 0 || runResult.Stdout != "run-ok" || runResult.Backend != sandbox.BackendLandlock {
		t.Fatalf("Run() = %+v, %v", runResult, err)
	}

	nonTTY, err := rt.Start(ctx, sandbox.CommandRequest{
		Command: "if IFS= read -r line; then printf unexpected; else printf stdin-eof; fi",
		Dir:     workspace, Constraints: constraints,
	})
	if err != nil {
		t.Fatalf("Start(non-TTY) error = %v", err)
	}
	status, err := nonTTY.Wait(ctx, 5*time.Second)
	if err != nil || status.Running || status.SupportsInput {
		t.Fatalf("non-TTY Wait() = %+v, %v", status, err)
	}
	nonTTYResult, err := nonTTY.Result(ctx)
	if err != nil || !strings.Contains(nonTTYResult.Stdout, "stdin-eof") {
		t.Fatalf("non-TTY Result() = %+v, %v", nonTTYResult, err)
	}

	ttySession, err := rt.Start(ctx, sandbox.CommandRequest{
		Command: "test -t 0 && stty raw -echo && value=$(dd bs=1 count=1 2>/dev/null) && printf 'got:%s' \"$value\"",
		Dir:     workspace, TTY: true, Constraints: constraints,
	})
	if err != nil {
		t.Fatalf("Start(TTY=true) error = %v", err)
	}
	status, err = ttySession.Status(ctx)
	if err != nil || !status.SupportsInput {
		t.Fatalf("TTY Status() = %+v, %v", status, err)
	}
	if err := ttySession.WriteInput(ctx, []byte("x")); err != nil {
		t.Fatalf("TTY WriteInput() error = %v", err)
	}
	status, err = ttySession.Wait(ctx, 5*time.Second)
	if err != nil || status.Running || status.SupportsInput {
		t.Fatalf("TTY Wait() = %+v, %v", status, err)
	}
	ttyResult, err := ttySession.Result(ctx)
	if err != nil || !strings.Contains(ttyResult.Stdout, "got:x") || ttyResult.Stderr != "" {
		t.Fatalf("TTY Result() = %+v, %v", ttyResult, err)
	}
}
