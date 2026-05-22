//go:build !windows

package setup

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

func Execute(Payload) error {
	return fmt.Errorf("windows setup: unsupported on %s", runtime.GOOS)
}

func ExecuteWithProgress(Payload, ProgressFunc) error {
	return fmt.Errorf("windows setup: unsupported on %s", runtime.GOOS)
}

func WithMaintenanceLock(context.Context, string, time.Duration, func() error) error {
	return fmt.Errorf("windows setup: unsupported on %s", runtime.GOOS)
}
