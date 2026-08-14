//go:build !windows

package cmdsession

import (
	"io"
	"os/exec"

	"github.com/creack/pty"
)

const (
	defaultTTYColumns = 80
	defaultTTYRows    = 24
)

func startTTYCommand(cmd *exec.Cmd) (io.ReadWriteCloser, error) {
	return pty.StartWithSize(cmd, &pty.Winsize{Cols: defaultTTYColumns, Rows: defaultTTYRows})
}
