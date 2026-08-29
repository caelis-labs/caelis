//go:build !windows

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
	"syscall"
)

// Acquire obtains an exclusive lock without replacing or deleting the lock
// file, so all waiters keep addressing the same inode. The context bounds
// waiting after observed contention; a successful non-blocking attempt wins a
// concurrent cancellation.
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
	err = waitForLock(ctx, func() (bool, error) {
		lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case lockErr == nil:
			return true, nil
		case errors.Is(lockErr, syscall.EWOULDBLOCK), errors.Is(lockErr, syscall.EAGAIN):
			return false, nil
		default:
			return false, lockErr
		}
	})
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &unixLock{file: file}, nil
}

type unixLock struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *unixLock) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.file != nil {
			l.err = errors.Join(syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN), l.file.Close())
		}
	})
	return l.err
}
