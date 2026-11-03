//go:build windows

package mcp

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func configureStdioProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// A stdio peer uses pipes, not a console. In particular, a detached Host
	// must not cause Windows to allocate a new console for this child.
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
