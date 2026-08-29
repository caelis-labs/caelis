//go:build windows

// Package filelock provides a small cross-process exclusive file lock.
package filelock

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

// Acquire obtains an exclusive lock without replacing or deleting the lock
// file, so all waiters keep addressing the same file identity. The context
// bounds waiting after observed contention; a successful non-blocking attempt
// wins a concurrent cancellation.
func Acquire(ctx context.Context, path string) (io.Closer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("filelock: path is empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY | windows.LOCKFILE_EXCLUSIVE_LOCK)
	overlapped := &windows.Overlapped{}
	err = waitForLock(ctx, func() (bool, error) {
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
	return &windowsLock{file: file, overlapped: overlapped}, nil
}

type windowsLock struct {
	file       *os.File
	overlapped *windows.Overlapped
	once       sync.Once
	err        error
}

func (l *windowsLock) Close() error {
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
