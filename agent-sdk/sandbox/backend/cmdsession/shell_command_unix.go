//go:build !windows

package cmdsession

import (
	"context"
	"os/exec"
)

func buildPlatformShellCommand(ctx context.Context, command string, _ bool) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "bash", "-lc", command), nil
}
