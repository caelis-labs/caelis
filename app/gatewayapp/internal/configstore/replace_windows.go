//go:build windows

package configstore

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

func replaceFileAtomic(source, destination string) error {
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
