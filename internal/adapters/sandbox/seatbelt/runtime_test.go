//go:build darwin

package seatbelt

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/internal/cmdsession"
	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/internal/runnerruntime"
)

func TestWaitSessionTimeoutDoesNotConsumeExitForLaterResultWait(t *testing.T) {
	dir := t.TempDir()
	runner := &seatbeltRunner{
		execCommand: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
			command := ""
			if len(args) > 0 {
				command = args[len(args)-1]
			}
			return exec.CommandContext(ctx, "sh", "-c", command)
		},
		goos:           runtime.GOOS,
		cfg:            sandbox.NormalizeConfig(sandbox.Config{CWD: dir}),
		sessionManager: cmdsession.NewSessionManager(cmdsession.DefaultSessionManagerConfig()),
	}
	t.Cleanup(func() { _ = runner.Close() })

	sessionID, err := runner.StartAsync(context.Background(), runnerruntime.Request{
		Command: "printf ok",
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartAsync() error = %v", err)
	}

	result, err := runner.WaitSession(context.Background(), sessionID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitSession(timeout) error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "ok" {
		t.Fatalf("stdout = %q, want ok", result.Stdout)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result, err = runner.WaitSession(waitCtx, sessionID, 0)
	if err != nil {
		t.Fatalf("WaitSession(result) error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "ok" {
		t.Fatalf("second stdout = %q, want ok", result.Stdout)
	}
}
