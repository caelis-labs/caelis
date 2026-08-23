//go:build windows

// Package atomicfile owns the platform-specific final replacement primitive
// shared by durable stores.
package atomicfile

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsReplaceRetryLimit = 20
	windowsReplaceRetryDelay = 10 * time.Millisecond
)

// Replace retries transient Windows sharing and lock failures while preserving
// the source temporary file for every retry attempt.
func Replace(source, destination string) error {
	var err error
	for attempt := 0; attempt < windowsReplaceRetryLimit; attempt++ {
		err = os.Rename(source, destination)
		if err == nil || !retryableWindowsReplaceError(err) {
			return err
		}
		if attempt+1 < windowsReplaceRetryLimit {
			time.Sleep(windowsReplaceRetryDelay)
		}
	}
	return err
}

func retryableWindowsReplaceError(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
