//go:build windows

package cmdsession

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/caelis-labs/caelis/impl/sandbox/internal/winps"
	"github.com/caelis-labs/caelis/internal/winproc"
)

func buildPlatformShellCommand(ctx context.Context, command string, tty bool) (*exec.Cmd, error) {
	if tty {
		return nil, fmt.Errorf("tty mode is not supported by cmdsession on windows")
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", winps.Args(command, winps.Options{})...)
	winproc.ConfigureHiddenConsole(cmd)
	return cmd, nil
}
