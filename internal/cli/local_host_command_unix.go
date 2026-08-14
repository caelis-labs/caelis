//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

func configureDetachedLocalHostCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
