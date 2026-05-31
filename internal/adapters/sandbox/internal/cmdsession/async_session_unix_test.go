//go:build !windows

package cmdsession

import (
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAsyncSessionCleansBackgroundProcessAfterShellExit(t *testing.T) {
	t.Parallel()

	session := NewAsyncSession(AsyncSessionConfig{
		Command:         "sleep 30 & printf 'bg:%s\\n' \"$!\"; exit 0",
		OutputBufferCap: 4096,
	})
	if err := session.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exitCode, err := session.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	stdout, _ := session.ReadAllOutput()
	pid := parseBackgroundPID(t, stdout)
	waitForProcessGone(t, pid)
}

func parseBackgroundPID(t *testing.T, stdout string) int {
	t.Helper()
	for _, field := range strings.Fields(stdout) {
		if !strings.HasPrefix(field, "bg:") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(field, "bg:"))
		if err != nil {
			t.Fatalf("parse background pid from %q: %v", field, err)
		}
		return pid
	}
	t.Fatalf("stdout = %q, want bg pid", stdout)
	return 0
}

func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still alive", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
