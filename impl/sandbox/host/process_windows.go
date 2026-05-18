//go:build windows

package host

import (
	"context"
	"os"
	"os/exec"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/winps"
)

func newShellCommand(ctx context.Context, command string, interactive bool) *exec.Cmd {
	return exec.CommandContext(ctx, "powershell.exe", winps.Args(command, winps.Options{Interactive: interactive})...)
}

func setProcessGroup(_ *exec.Cmd) {
}

func killProcessTree(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	return proc.Kill()
}
