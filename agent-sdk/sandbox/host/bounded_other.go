//go:build !windows

package host

import (
	"context"
	"errors"
	"os/exec"
)

func runBoundedWindowsCommand(context.Context, *exec.Cmd, []byte) (int, error) {
	return -1, errors.New("host: Windows Job Objects are unavailable")
}
