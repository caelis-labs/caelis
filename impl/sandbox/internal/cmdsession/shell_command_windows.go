//go:build windows

package cmdsession

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/winps"
)

func buildPlatformShellCommand(ctx context.Context, command string, tty bool) (*exec.Cmd, error) {
	if tty {
		return nil, fmt.Errorf("tty mode is not supported by cmdsession on windows")
	}
	return exec.CommandContext(ctx, "powershell.exe", winps.Args(command, winps.Options{})...), nil
}
