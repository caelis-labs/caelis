//go:build !windows

package win32

import (
	"fmt"
	"os/exec"
	"runtime"
)

type Token uintptr

type ProcessIdentity struct {
	PID          uint32 `json:"pid"`
	CreationTime uint64 `json:"creation_time"`
}

func CurrentUserLocalAppData() (string, error) {
	return "", fmt.Errorf("win32: LocalAppData known folder unsupported on %s", runtime.GOOS)
}

func CurrentProcessIdentity() (ProcessIdentity, error) {
	return ProcessIdentity{}, fmt.Errorf("win32: process identity unsupported on %s", runtime.GOOS)
}

func ProcessIdentityAlive(ProcessIdentity) (bool, error) {
	return false, fmt.Errorf("win32: process identity inspection unsupported on %s", runtime.GOOS)
}

func CurrentProcessUserSID() (string, error) {
	return "", fmt.Errorf("win32: current process user SID unsupported on %s", runtime.GOOS)
}

func NormalizeSID(string) (string, error) {
	return "", fmt.Errorf("win32: SID normalization unsupported on %s", runtime.GOOS)
}

func RestrictedCurrentProcessTokenWithSIDs([]string) (Token, error) {
	return 0, fmt.Errorf("win32: restricted token unsupported on %s", runtime.GOOS)
}

func (t Token) Close() error {
	return nil
}

func ConfigureHiddenConsole(*exec.Cmd) {}
