//go:build windows

package file

import (
	"os"
	"path/filepath"
)

func lockSessionStoreRoot(root string) (*os.File, error) {
	return os.OpenFile(filepath.Join(root, lockFilename), os.O_CREATE|os.O_RDWR, 0o600)
}

func unlockSessionStoreRoot(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
