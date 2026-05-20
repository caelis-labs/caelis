//go:build !windows

package cmdsession

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

func buildPlatformShellCommand(ctx context.Context, command string, tty bool) (*exec.Cmd, error) {
	if !tty {
		return exec.CommandContext(ctx, "bash", "-lc", command), nil
	}
	scriptPath, err := exec.LookPath("script")
	if err != nil {
		return nil, fmt.Errorf("failed to locate script utility for tty mode: %w", err)
	}
	if runtime.GOOS == "linux" {
		return exec.CommandContext(ctx, scriptPath, "-qefc", command, "/dev/null"), nil
	}
	return exec.CommandContext(ctx, scriptPath, "-q", "/dev/null", "bash", "-lc", command), nil
}
