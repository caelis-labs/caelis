//go:build !linux && !windows

package processtest

import (
	"errors"
	"syscall"
)

func exited(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}
