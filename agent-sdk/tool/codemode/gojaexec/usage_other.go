//go:build !darwin && !linux && !windows

package gojaexec

import (
	"os"
	"time"
)

func processCPU() time.Duration      { return 0 }
func peakRSS(*os.ProcessState) int64 { return 0 }
func cpuSupported() bool             { return false }
