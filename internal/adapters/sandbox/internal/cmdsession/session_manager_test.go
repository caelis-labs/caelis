package cmdsession

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSessionManagerContextTimeoutWaitPreservesExitForLaterWait(t *testing.T) {
	manager := NewSessionManager(DefaultSessionManagerConfig())
	t.Cleanup(func() { _ = manager.Close() })

	session, err := manager.StartSession(AsyncSessionConfig{
		Command: "printf ok",
		BuildCommand: func(ctx context.Context, _ AsyncSessionConfig) (*exec.Cmd, error) {
			if runtime.GOOS == "windows" {
				return exec.CommandContext(ctx, "cmd.exe", "/d", "/c", "echo ok"), nil
			}
			return exec.CommandContext(ctx, "sh", "-c", "printf ok"), nil
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	if _, err := manager.WaitSessionWithContextTimeout(context.Background(), session.ID, 5*time.Second); err != nil {
		t.Fatalf("WaitSessionWithContextTimeout() error = %v", err)
	}
	result, err := manager.GetResult(session.ID)
	if err != nil {
		t.Fatalf("GetResult() error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "ok" {
		t.Fatalf("stdout = %q, want ok", result.Stdout)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := manager.WaitSession(waitCtx, session.ID); err != nil {
		t.Fatalf("second WaitSession() error = %v", err)
	}
}
