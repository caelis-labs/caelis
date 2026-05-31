//go:build windows

package procutil

import (
	"errors"
	"os/exec"
)

func SetProcessGroup(cmd *exec.Cmd) {
	// No-op on Windows. We fall back to killing the direct process.
}

func KillProcessGroup(pid int) error {
	return errors.New("process groups are not supported on windows")
}
