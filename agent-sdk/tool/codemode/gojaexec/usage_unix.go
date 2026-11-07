//go:build darwin || linux

package gojaexec

import (
	"os"
	"runtime"
	"syscall"
	"time"
)

func processCPU() time.Duration {
	var u syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &u) != nil {
		return 0
	}
	return time.Duration(u.Utime.Sec+u.Stime.Sec)*time.Second + time.Duration(u.Utime.Usec+u.Stime.Usec)*time.Microsecond
}

func peakRSS(s *os.ProcessState) int64 {
	u, ok := s.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return u.Maxrss
	}
	return u.Maxrss * 1024
}

func cpuSupported() bool { return true }
