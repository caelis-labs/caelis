//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gatewayapp

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
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
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return diagnosticLockHeld
		}
		return diagnosticLockUnknown
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		return diagnosticLockUnknown
	}
	return diagnosticLockFree
}
