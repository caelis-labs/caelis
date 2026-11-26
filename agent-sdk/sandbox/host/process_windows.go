//go:build windows

package host

import (
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/internal/winproc"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/internal/winps"
)

const createNoWindow = 0x08000000

func newShellCommand(ctx context.Context, command string, interactive bool, _ *sandbox.ExecutionConfig) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell.exe", winps.Args(command, winps.Options{Interactive: interactive, SkipModulePrelude: true})...)
	configureHiddenConsole(cmd)
	return cmd
}

func setProcessGroup(_ *exec.Cmd) {
}

func hostTTYSupported() bool { return true }

func startHostTTYCommand(cmd *exec.Cmd) (io.ReadWriteCloser, hostTTYProcess, error) {
	process, err := winproc.StartConPTY(winproc.ConPTYConfig{
		Application: cmd.Path,
		Args:        append([]string(nil), cmd.Args[1:]...),
		Dir:         cmd.Dir,
		Env:         append([]string(nil), cmd.Env...),
	})
	if err != nil {
		return nil, nil, err
	}
	return struct {
		io.Reader
		io.Writer
		io.Closer
	}{Reader: process.Output(), Writer: process.Input(), Closer: process.Input()}, process, nil
}

func killProcessTree(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	return proc.Kill()
}

func configureHiddenConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
