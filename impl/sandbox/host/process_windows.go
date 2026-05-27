//go:build windows

package host

import (
	"context"
	"os"
	"os/exec"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/winps"
	"github.com/OnslaughtSnail/caelis/internal/winproc"
)

func newShellCommand(ctx context.Context, command string, interactive bool) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell.exe", winps.Args(command, winps.Options{Interactive: interactive})...)
	winproc.ConfigureHiddenConsole(cmd)
	return cmd
}

func setProcessGroup(_ *exec.Cmd) {
}

func killProcessTree(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	return proc.Kill()
}
