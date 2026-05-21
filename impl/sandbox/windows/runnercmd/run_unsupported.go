//go:build !windows

package runnercmd

import (
	"fmt"
	"io"
	"runtime"
)

func Run(_ io.Reader, _ io.Writer, stderr io.Writer) int {
	fmt.Fprintf(stderr, "caelis command runner is only supported on windows (current=%s)\n", runtime.GOOS)
	return 1
}
