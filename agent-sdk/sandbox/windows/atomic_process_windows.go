//go:build windows

package windows

import (
	"context"
	"io"
	"os/exec"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox/internal/winproc"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/win32"
)

func runAtomicJobProcess(ctx context.Context, cmd *exec.Cmd, token win32.Token, stdin []byte, stdout, stderr io.Writer, releaseUse func(), bounded bool) (int, error, bool) {
	if err := ctx.Err(); err != nil {
		_ = token.Close()
		return -1, err, false
	}
	process, err := winproc.StartJobProcess(winproc.JobProcessConfig{Token: uintptr(token), Application: cmd.Path, Args: append([]string(nil), cmd.Args[1:]...), Dir: cmd.Dir, Env: append([]string(nil), cmd.Env...), Stdin: len(stdin) > 0})
	_ = token.Close()
	if err != nil {
		return -1, err, false
	}
	drain := windowsPreflightTimeout
	if bounded {
		drain = windowsTerminateDrain
	}
	return winproc.RunStartedJobProcess(ctx, process, stdin, stdout, stderr, releaseUse, windowsTerminateDrain, drain, bounded)
}
