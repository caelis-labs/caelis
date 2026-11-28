package bootstrap

import (
	"io"

	"github.com/caelis-labs/caelis/internal/shellproc"
)

// RunShellWorker handles the private Unix Shell entry before Host startup.
func RunShellWorker(args []string, stdin io.Reader, stdout io.Writer) (int, bool) {
	if len(args) != 1 || args[0] != shellproc.WorkerArgument {
		return 0, false
	}
	return shellproc.WorkerMain(stdin, stdout), true
}
