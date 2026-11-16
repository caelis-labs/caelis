//go:build windows

package gojaexec

import (
	"os/exec"
	"syscall"
)

func configureWorkerProcess(cmd *exec.Cmd) {
	// Workers communicate through pipes and must not acquire a console when
	// their Host is detached from the user's terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
