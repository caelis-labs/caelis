//go:build windows

package gatewayapp

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// probeOwnerLock checks a pre-existing lock without creating the lock file or
// touching the database. A free result only describes the instant of the
// probe; it is not a lease for a later recovery operation.
func probeOwnerLock(path string) string {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return diagnosticLockUnknown
	}
	defer file.Close()
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return diagnosticLockHeld
		}
		return diagnosticLockUnknown
	}
	if err := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped); err != nil {
		return diagnosticLockUnknown
	}
	return diagnosticLockFree
}
