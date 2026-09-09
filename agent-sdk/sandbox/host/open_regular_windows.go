//go:build windows

package host

import (
	"os"

	"golang.org/x/sys/windows"
)

// openRegularFile opens path and rejects non-disk/non-regular handles. Windows
// has no portable nonblocking file open here, so a named pipe may still block
// in os.Open before this check runs.
func openRegularFile(path string) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if err := rejectNonRegularOpenedFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func rejectNonRegularPlatformFile(file *os.File) error {
	if file == nil {
		return errNotRegularFile
	}
	kind, err := windows.GetFileType(windows.Handle(file.Fd()))
	if err != nil {
		return err
	}
	if kind != windows.FILE_TYPE_DISK {
		return notRegularFileError(file.Name())
	}
	return nil
}
