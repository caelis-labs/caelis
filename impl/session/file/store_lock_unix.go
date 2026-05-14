//go:build !windows

package file

import (
	"os"
	"path/filepath"
	"syscall"
)

func lockSessionStoreRoot(root string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(root, lockFilename), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func unlockSessionStoreRoot(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
