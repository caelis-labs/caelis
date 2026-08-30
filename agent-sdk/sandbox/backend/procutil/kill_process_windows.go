//go:build windows

package procutil

import "os/exec"

// KillProcess terminates the direct process on Windows, where this backend
// does not own an operating-system process-group primitive.
func KillProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
