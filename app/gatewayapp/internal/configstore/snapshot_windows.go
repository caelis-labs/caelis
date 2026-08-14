//go:build windows

package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

const windowsInPlaceSnapshotTimeout = 250 * time.Millisecond

// readAppConfigSnapshot normally reads without the writer lock. The bounded
// retry is used only when Windows exposed the temporary invalid bytes from the
// compatibility in-place replacement path after an atomic rename failed.
func readAppConfigSnapshot(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Valid(data) {
		return data, err
	}

	retryCtx, cancel := context.WithTimeout(ctx, windowsInPlaceSnapshotTimeout)
	defer cancel()
	lock, lockErr := acquireFileLock(retryCtx, path+".lock")
	if lockErr != nil {
		return nil, fmt.Errorf("gatewayapp: wait for bounded Windows app config replacement: %w", lockErr)
	}
	data, readErr := os.ReadFile(path)
	return data, errors.Join(readErr, lock.Close())
}
