package file

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionStoreRootLockHonorsContextCancellation(t *testing.T) {
	root := t.TempDir()
	first, err := lockSessionStoreRoot(context.Background(), root, storeRootLockExclusive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unlockSessionStoreRoot(first); err != nil {
			t.Errorf("unlock first session store root: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	second, err := lockSessionStoreRoot(ctx, root, storeRootLockExclusive)
	if second != nil {
		_ = unlockSessionStoreRoot(second)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("cancelled lock wait took %v", elapsed)
	}
}
