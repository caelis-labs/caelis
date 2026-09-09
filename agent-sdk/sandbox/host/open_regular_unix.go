//go:build unix

package host

import (
	"os"

	"golang.org/x/sys/unix"
)

// openRegularFile opens path for reading only when the opened fd is a regular
// file. O_NONBLOCK avoids blocking on FIFO/device replacement between Stat and
// Open; the flag is cleared after the fd check.
func openRegularFile(path string) (*os.File, error) {
	var fd int
	var err error
	for {
		fd, err = unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: unix.EINVAL}
	}
	if err := rejectNonRegularOpenedFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = file.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return file, nil
}

func rejectNonRegularPlatformFile(*os.File) error {
	return nil
}
