//go:build !windows

package acp

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func duplicateServerFile(file *os.File, direction string) (*os.File, error) {
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("acp: duplicate server %s: %w", direction, err)
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}
