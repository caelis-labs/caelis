//go:build !windows

package host

import (
	"errors"
	"syscall"

	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/internal/procutil"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func killProcessID(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := procutil.KillProcessGroup(pid); err == nil {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}
