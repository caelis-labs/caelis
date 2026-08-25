//go:build windows

package acp

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func duplicateServerFile(file *os.File, direction string) (*os.File, error) {
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(
		process,
		windows.Handle(file.Fd()),
		process,
		&duplicate,
		0,
		false,
		windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		return nil, fmt.Errorf("acp: duplicate server %s: %w", direction, err)
	}
	return os.NewFile(uintptr(duplicate), file.Name()), nil
}
