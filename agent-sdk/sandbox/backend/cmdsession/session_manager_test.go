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

func TestAsyncSessionNonTTYClosesStdin(t *testing.T) {
	t.Parallel()

	manager := NewSessionManager(DefaultSessionManagerConfig())
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.StartSession(AsyncSessionConfig{
		Command: "read stdin",
		TTY:     false,
		BuildCommand: func(ctx context.Context, _ AsyncSessionConfig) (*exec.Cmd, error) {
			if runtime.GOOS == "windows" {
				return exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$text = [Console]::In.ReadToEnd(); if ($text.Length -eq 0) { Write-Output 'stdin-eof' } else { Write-Output 'unexpected-input' }; Start-Sleep -Milliseconds 200"), nil
			}
			return exec.CommandContext(ctx, "sh", "-c", "if IFS= read -r line; then printf 'unexpected-input'; else printf 'stdin-eof'; fi; sleep 0.2"), nil
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if err := session.WriteInput([]byte("ignored\n")); err == nil || !strings.Contains(err.Error(), "stdin not available") {
		t.Fatalf("WriteInput() error = %v, want stdin not available", err)
	}
	if _, err := manager.WaitSessionWithContextTimeout(context.Background(), session.ID, 5*time.Second); err != nil {
		t.Fatalf("WaitSessionWithContextTimeout() error = %v", err)
	}
	result, err := manager.GetResult(session.ID)
	if err != nil {
		t.Fatalf("GetResult() error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "stdin-eof" {
		t.Fatalf("stdout = %q, want stdin-eof", result.Stdout)
	}
}
