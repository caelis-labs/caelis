//go:build !windows

package win32

import (
	"fmt"
	"os/exec"
	"runtime"
)

type Token uintptr

func RestrictedCurrentProcessTokenWithSIDs([]string) (Token, error) {
	return 0, fmt.Errorf("win32: restricted token unsupported on %s", runtime.GOOS)
}

func (t Token) Close() error {
	return nil
}

func ConfigureHiddenConsole(*exec.Cmd) {}
