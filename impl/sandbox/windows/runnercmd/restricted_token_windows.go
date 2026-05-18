//go:build windows

package runnercmd

import (
	"fmt"
	"os/exec"
	"syscall"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
)

func restrictedToken(capabilitySIDs []string) (win32.Token, func(), error) {
	_ = win32.AllowNullDeviceForSIDs(capabilitySIDs)
	token, err := win32.RestrictedCurrentProcessTokenWithSIDs(capabilitySIDs)
	if err != nil {
		return 0, nil, fmt.Errorf("create restricted token: %w", err)
	}
	return token, func() { _ = token.Close() }, nil
}

func prepareRestrictedToken(cmd *exec.Cmd, capabilitySIDs []string) (func(), error) {
	token, release, err := restrictedToken(capabilitySIDs)
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(token)}
	return release, nil
}
