//go:build !windows

package adapterhost

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

type processTree struct{}

func prepareProcess(command *exec.Cmd) (*processTree, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &processTree{}, nil
}

func (*processTree) Started(*exec.Cmd) error { return nil }

func (*processTree) Close() error { return nil }

func signalProcess(_ context.Context, command *exec.Cmd, _ *processTree) error {
	if command == nil || command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func killProcess(command *exec.Cmd, _ *processTree) error {
	if command == nil || command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
