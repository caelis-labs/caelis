//go:build windows

package winexec

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunTimesOutAndReturns(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only process timeout test")
	}
	started := time.Now()
	result, err := Run(context.Background(), "cmd.exe", []string{
		"/d", "/c", "ping -n 30 127.0.0.1 >nul",
	}, Options{Timeout: 500 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	if !result.TimedOut {
		t.Fatalf("Run() TimedOut = false, want true")
	}
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("Run() returned after %s, want bounded timeout", elapsed)
	}
}

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only process output test")
	}
	result, err := Run(context.Background(), "cmd.exe", []string{
		"/d", "/c", "echo alpha& echo beta 1>&2& exit /b 7",
	}, Options{Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("Run() error = nil, want non-zero exit")
	}
	if result.ExitCode != 7 {
		t.Fatalf("Run() ExitCode = %d, want 7", result.ExitCode)
	}
	combined := string(result.CombinedOutput())
	if !strings.Contains(combined, "alpha") || !strings.Contains(combined, "beta") {
		t.Fatalf("Run() output = %q, want stdout and stderr tails", combined)
	}
}
