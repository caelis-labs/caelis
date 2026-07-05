//go:build windows

package host

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func newShellCommand(ctx context.Context, command string, interactive bool) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell.exe", powershellArgs(command, powershellOptions{Interactive: interactive})...)
	configureHiddenConsole(cmd)
	return cmd
}

func setProcessGroup(_ *exec.Cmd) {
}

func killProcessTree(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	return proc.Kill()
}

func configureHiddenConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
