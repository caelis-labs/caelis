//go:build !windows

package host

import (
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

const (
	hostTTYColumns = 80
	hostTTYRows    = 24
)

func newShellCommand(ctx context.Context, command string, _ bool) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func hostTTYSupported() bool { return true }

func startHostTTYCommand(cmd *exec.Cmd) (io.ReadWriteCloser, hostTTYProcess, error) {
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: hostTTYColumns, Rows: hostTTYRows})
	return terminal, nil, err
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
