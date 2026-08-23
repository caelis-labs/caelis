//go:build windows

package capability

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type storeFileLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func tryAcquireStoreFileLock(path string) (*storeFileLock, bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	lock := &storeFileLock{file: file}
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&lock.overlapped,
	)
	if err == nil {
		return lock, false, nil
	}
	_ = file.Close()
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return nil, true, nil
	}
	return nil, false, err
}

func releaseStoreFileLock(lock *storeFileLock) error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := windows.UnlockFileEx(windows.Handle(lock.file.Fd()), 0, 1, 0, &lock.overlapped)
	closeErr := lock.file.Close()
	return errors.Join(unlockErr, closeErr)
}
