package file

import (
	"context"
	"time"
)

const ownerLockRetryInterval = 25 * time.Millisecond

func waitForOwnerLock(ctx context.Context, try func() (bool, error)) error {
	for {
		acquired, err := try()
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
		timer := time.NewTimer(ownerLockRetryInterval)
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
