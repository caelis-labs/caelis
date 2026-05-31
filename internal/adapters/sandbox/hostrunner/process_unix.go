//go:build !windows

package host

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func newShellCommand(ctx context.Context, command string, _ bool) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil {
		return proc.Kill()
	}
	return nil
}
