//go:build windows

package file

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

func acquireOwnerLock(ctx context.Context, root *os.Root, path string) (io.Closer, error) {
	file, err := root.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := validateOpenedRegular(root, path, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY | windows.LOCKFILE_EXCLUSIVE_LOCK)
	overlapped := &windows.Overlapped{}
	err = waitForOwnerLock(ctx, func() (bool, error) {
		lockErr := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, overlapped)
		switch {
		case lockErr == nil:
			return true, nil
		case errors.Is(lockErr, windows.ERROR_LOCK_VIOLATION), errors.Is(lockErr, windows.ERROR_IO_PENDING):
			return false, nil
		default:
			return false, lockErr
		}
	})
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := validateOpenedRegular(root, path, file); err != nil {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
		_ = file.Close()
		return nil, err
	}
	return &windowsOwnerLock{file: file, overlapped: overlapped}, nil
}

type windowsOwnerLock struct {
	file       *os.File
	overlapped *windows.Overlapped
	once       sync.Once
	err        error
}

func (l *windowsOwnerLock) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.file != nil {
			l.err = errors.Join(windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, l.overlapped), l.file.Close())
		}
	})
	return l.err
}
