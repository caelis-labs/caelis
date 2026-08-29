package filelock

import (
	"context"
	"time"
)

const retryInterval = 25 * time.Millisecond

// waitForLock lets a successful non-blocking attempt win a concurrent context
// cancellation. The caller checks the context before preparing the lock file;
// after an attempt observes contention, the context bounds every later retry.
func waitForLock(ctx context.Context, try func() (bool, error)) error {
	for {
		acquired, err := try()
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}

		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}
