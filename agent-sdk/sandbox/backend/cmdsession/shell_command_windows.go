//go:build windows

package cmdsession

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
)

const createNoWindow = 0x08000000

func buildPlatformShellCommand(ctx context.Context, command string, tty bool) (*exec.Cmd, error) {
	if tty {
		return nil, fmt.Errorf("tty mode is not supported by cmdsession on windows")
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", host.PowerShellExecArgs(command)...)
	configureHiddenConsole(cmd)
	return cmd, nil
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
