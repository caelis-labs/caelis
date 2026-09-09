//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gatewayapp

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProbeOwnerLockDistinguishesHeldLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memoryd.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if got := probeOwnerLock(path); got != diagnosticLockHeld {
		t.Fatalf("probeOwnerLock() = %q, want %q", got, diagnosticLockHeld)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if got := probeOwnerLock(path); got != diagnosticLockFree {
		t.Fatalf("probeOwnerLock() after release = %q, want %q", got, diagnosticLockFree)
	}
}
