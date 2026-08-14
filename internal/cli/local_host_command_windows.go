//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

func configureDetachedLocalHostCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true, CreationFlags: detachedProcess | createNewProcessGroup,
	}
}
