package host

import (
	"fmt"
	"io/fs"
	"os"
)

// errNotRegularFile is returned when Open targets a FIFO, device, socket, or
// other non-regular file. Unix Open uses O_NONBLOCK so those paths do not
// block; Windows still uses os.Open, so a named pipe can block until the other
// end connects, then this check rejects the handle.
var errNotRegularFile = fmt.Errorf("%w: not a regular file", fs.ErrInvalid)

func rejectNonRegularOpenedFile(file *os.File) error {
	if file == nil {
		return errNotRegularFile
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return notRegularFileError(file.Name())
	}
	return rejectNonRegularPlatformFile(file)
}

func notRegularFileError(path string) error {
	if path == "" {
		return errNotRegularFile
	}
	return fmt.Errorf("%s: %w", path, errNotRegularFile)
}
