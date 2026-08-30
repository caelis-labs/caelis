//go:build !windows

package procutil

import "os/exec"

// KillProcess terminates the process group when available so descendants do
// not retain stdout or stderr pipes, then falls back to the direct process.
func KillProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := KillProcessGroup(cmd.Process.Pid); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
