//go:build linux

package bwrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestSandboxedCommandSmoke(t *testing.T) {
	if os.Getenv("CAELIS_LINUX_SANDBOX_SMOKE_E2E") != "1" {
		t.Skip("set CAELIS_LINUX_SANDBOX_SMOKE_E2E=1 to run the Linux bwrap sandbox smoke test")
	}

	workspace := t.TempDir()
	rt, err := New(sandbox.Config{CWD: workspace})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer rt.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	constraints := sandbox.Constraints{
		Route:      sandbox.RouteSandbox,
		Backend:    sandbox.BackendBwrap,
		Permission: sandbox.PermissionWorkspaceWrite,
		Network:    sandbox.NetworkDisabled,
	}

	session, err := rt.Start(ctx, sandbox.CommandRequest{
		Command:     "if IFS= read -r line; then printf 'unexpected-input'; else printf 'stdin-eof'; fi; sleep 0.2",
		Dir:         workspace,
		Constraints: constraints,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	status, err := session.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.SupportsInput {
		t.Fatalf("Status() = %+v, want SupportsInput=false", status)
	}
	if err := session.WriteInput(ctx, []byte("ignored\n")); err == nil {
		t.Fatal("WriteInput() error = nil, want non-TTY stdin rejection")
	}

	var stdout strings.Builder
	cursor := sandbox.OutputCursor{}
	for {
		observation, err := session.AwaitOutput(ctx, cursor)
		if err != nil {
			t.Fatalf("AwaitOutput(%+v) error = %v", cursor, err)
		}
		out, _, nextStdout, nextStderr, err := session.ReadOutput(ctx, cursor.Stdout, cursor.Stderr)
		if err != nil {
			t.Fatalf("ReadOutput(%+v) error = %v", cursor, err)
		}
		stdout.Write(out)
		cursor = sandbox.OutputCursor{Stdout: nextStdout, Stderr: nextStderr}
		if !observation.Status.Running {
			if cursor != observation.Cursor {
				t.Fatalf("terminal cursor = %+v, observation cursor = %+v", cursor, observation.Cursor)
			}
			break
		}
	}
	result, err := session.Result(ctx)
	if err != nil {
		t.Fatalf("Result() error = %v; result=%+v", err, result)
	}
	if got := strings.TrimSpace(stdout.String()); got != "stdin-eof" {
		t.Fatalf("observed stdout = %q, want stdin-eof", stdout.String())
	}
	if result.Backend != sandbox.BackendBwrap || result.Route != sandbox.RouteSandbox {
		t.Fatalf("Result() = %+v, want bwrap sandbox route", result)
	}

	result, err = rt.Run(ctx, sandbox.CommandRequest{
		Command:     "printf ok > smoke.txt; cat smoke.txt",
		Dir:         workspace,
		Constraints: constraints,
	})
	if err != nil || strings.TrimSpace(result.Stdout) != "ok" {
		t.Fatalf("workspace write smoke error = %v; result=%+v", err, result)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	deniedTarget := filepath.Join(home, ".caelis-bwrap-deny-"+time.Now().Format("20060102150405.000000000"))
	t.Cleanup(func() { _ = os.Remove(deniedTarget) })
	result, err = rt.Run(ctx, sandbox.CommandRequest{
		Command:     "printf denied > \"$CAELIS_DENIED_TARGET\"",
		Dir:         workspace,
		Env:         map[string]string{"CAELIS_DENIED_TARGET": deniedTarget},
		Constraints: constraints,
	})
	if err == nil {
		t.Fatalf("outside-workspace write unexpectedly succeeded: result=%+v", result)
	}
	if _, statErr := os.Stat(deniedTarget); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside-workspace target stat error = %v, want not exist", statErr)
	}

	ttySession, err := rt.Start(ctx, sandbox.CommandRequest{
		Command:     "test -t 0 && stty raw -echo && value=$(dd bs=1 count=1 2>/dev/null) && printf 'got:%s' \"$value\"",
		Dir:         workspace,
		TTY:         true,
		Constraints: constraints,
	})
	if err != nil {
		t.Fatalf("Start(TTY=true) error = %v", err)
	}
	status, err = ttySession.Status(ctx)
	if err != nil {
		t.Fatalf("TTY Status() error = %v", err)
	}
	if !status.SupportsInput {
		t.Fatalf("TTY Status() = %+v, want SupportsInput=true", status)
	}
	if err := ttySession.WriteInput(ctx, []byte("x")); err != nil {
		t.Fatalf("TTY WriteInput() error = %v", err)
	}
	status, err = ttySession.Wait(ctx, 5*time.Second)
	if err != nil {
		t.Fatalf("TTY Wait() error = %v", err)
	}
	if status.Running || status.SupportsInput {
		t.Fatalf("TTY terminal Status() = %+v", status)
	}
	ttyResult, err := ttySession.Result(ctx)
	if err != nil {
		t.Fatalf("TTY Result() error = %v; result=%+v", err, ttyResult)
	}
	if !strings.Contains(ttyResult.Stdout, "got:x") || ttyResult.Stderr != "" {
		t.Fatalf("TTY Result() = %+v, want merged got:x output", ttyResult)
	}
}
