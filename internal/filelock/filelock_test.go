package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireRejectsCanceledContextBeforePreparingLockFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "nested", "authority.lock")

	lock, err := Acquire(ctx, path)
	if lock != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire() = %T, %v, want nil, context.Canceled", lock, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock path after canceled Acquire() = %v, want not created", err)
	}
}

func TestWaitForLockLetsSuccessfulAttemptWinConcurrentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := waitForLock(ctx, func() (bool, error) {
		attempts++
		cancel()
		return true, nil
	})
	if err != nil || attempts != 1 {
		t.Fatalf("waitForLock() = %v after %d attempts, want one successful attempt", err, attempts)
	}
}

func TestWaitForLockStopsCanceledWaitAfterContention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := waitForLock(ctx, func() (bool, error) {
		attempts++
		cancel()
		return false, nil
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("waitForLock() = %v after %d attempts, want context.Canceled after one busy attempt", err, attempts)
	}
}
