//go:build !windows

package runnercmd

import (
	"os/exec"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
)

func restrictedToken([]string) (win32.Token, func(), error) {
	return 0, func() {}, nil
}

func prepareRestrictedToken(*exec.Cmd, []string) (func(), error) {
	return func() {}, nil
}
