package gojaexec

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func processCPU() time.Duration {
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user) != nil {
		return 0
	}
	ticks := uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)
	ticks += uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime)
	return time.Duration(ticks) * 100 * time.Nanosecond
}

// Windows ProcessState does not expose a peak working set. Zero means this
// diagnostic is unavailable; it is never used as a memory admission guarantee.
func peakRSS(*os.ProcessState) int64 { return 0 }
func cpuSupported() bool             { return true }
