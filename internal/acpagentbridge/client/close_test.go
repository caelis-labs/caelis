package client

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

const closeProcessHelperEnv = "CAELIS_ACP_CLOSE_HELPER"

func TestCloseAcceptsProvenForcedProcessTermination(t *testing.T) {
	acpClient := startCloseProcessHelper(t, "slow-exit")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := acpClient.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v, want proven forced cleanup", err)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("Close() elapsed = %v, want prompt forced cleanup", elapsed)
	}
	if err := acpClient.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v, want stable terminal result", err)
	}
}

func TestCloseAcceptsProvenNonZeroProcessExit(t *testing.T) {
	acpClient := startCloseProcessHelper(t, "nonzero-exit")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := acpClient.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v, want terminal process proof despite exit status", err)
	}
}

func TestUnexpectedProcessShutdownErrorPreservesForcedCleanupFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	forceErr := errors.New("terminate process tree failed")
	got := unexpectedProcessShutdownError(ctx, errors.Join(context.Cause(ctx), forceErr))
	if !errors.Is(got, forceErr) || errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("unexpectedProcessShutdownError() = %v, want only forced cleanup failure", got)
	}
}

func TestCompleteProcessShutdownErrorPreservesForcedReleaseFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	releaseErr := errors.New("release process-tree handle failed")
	waited := false
	got := completeProcessShutdownError(ctx, context.Cause(ctx), func() error {
		waited = true
		return releaseErr
	})
	if !waited {
		t.Fatal("completeProcessShutdownError() did not observe the forced process wait result")
	}
	if !errors.Is(got, releaseErr) || errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("completeProcessShutdownError() = %v, want only process release failure", got)
	}
}

func TestCompleteProcessShutdownErrorDoesNotWaitAfterGracefulExit(t *testing.T) {
	waited := false
	if err := completeProcessShutdownError(context.Background(), nil, func() error {
		waited = true
		return errors.New("unexpected wait")
	}); err != nil {
		t.Fatalf("completeProcessShutdownError() error = %v", err)
	}
	if waited {
		t.Fatal("completeProcessShutdownError() observed wait result after graceful exit")
	}
}

func startCloseProcessHelper(t *testing.T, mode string) *Client {
	t.Helper()
	acpClient, err := Start(context.Background(), Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestCloseProcessHelper$"},
		Env:     map[string]string{closeProcessHelperEnv: mode},
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return acpClient
}

func TestCloseProcessHelper(t *testing.T) {
	mode := os.Getenv(closeProcessHelperEnv)
	if mode == "" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	switch mode {
	case "slow-exit":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "nonzero-exit":
		os.Exit(7)
	default:
		os.Exit(9)
	}
}
