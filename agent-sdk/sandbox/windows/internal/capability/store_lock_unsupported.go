//go:build !windows

package capability

import (
	"errors"
	"os"
)

type storeFileLock struct {
	file *os.File
	path string
}

func tryAcquireStoreFileLock(path string) (*storeFileLock, bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return &storeFileLock{file: file, path: path}, false, nil
	}
	if errors.Is(err, os.ErrExist) || os.IsExist(err) {
		return nil, true, nil
	}
	return nil, false, err
}

func releaseStoreFileLock(lock *storeFileLock) error {
	if lock == nil {
		return nil
	}
	var closeErr error
	if lock.file != nil {
		closeErr = lock.file.Close()
	}
	return errors.Join(closeErr, os.Remove(lock.path))
}
