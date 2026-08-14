//go:build windows

package cmdsession

import (
	"fmt"
	"io"
	"os/exec"
)

func startTTYCommand(_ *exec.Cmd) (io.ReadWriteCloser, error) {
	return nil, fmt.Errorf("tty mode is not supported by cmdsession on windows")
}
