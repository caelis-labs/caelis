//go:build unix

package bot

import (
	"os"

	"golang.org/x/sys/unix"
)

// openFilesRegular opens rel beneath the confined root with O_NONBLOCK and
// then requires a regular opened file. A path replaced by a FIFO or device
// after validation therefore cannot block the open, and a special opened file
// is rejected before any read. Blocking reads are restored before returning.
func openFilesRegular(root *os.Root, rel string) (*os.File, error) {
	file, err := root.OpenFile(rel, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errFilesNotRegular
	}
	if err := unix.SetNonblock(int(file.Fd()), false); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
