//go:build !windows

// Package processtest provides process-lifecycle assertions for sandbox tests.
package processtest

import (
	"fmt"
	"time"
)

// WaitForExit asserts termination within the supplied deadline. On Linux a
// zombie has terminated even if its parent (including a container's PID 1)
// has not reaped it. A live process or an unreadable state still fails.
func WaitForExit(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return fmt.Errorf("invalid process PID %d", pid)
	}
	deadline := time.Now().Add(timeout)
	for {
		stopped, err := exited(pid)
		if err != nil {
			return fmt.Errorf("inspect process %d: %w", pid, err)
		}
		if stopped {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process %d is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
