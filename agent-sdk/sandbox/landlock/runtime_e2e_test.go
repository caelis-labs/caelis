//go:build linux

package landlock

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

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
