//go:build !windows

package setup

import (
	"fmt"
	"runtime"
)

func Execute(Payload) error {
	return fmt.Errorf("windows setup: unsupported on %s", runtime.GOOS)
}

func ExecuteWithProgress(Payload, ProgressFunc) error {
	return fmt.Errorf("windows setup: unsupported on %s", runtime.GOOS)
}
