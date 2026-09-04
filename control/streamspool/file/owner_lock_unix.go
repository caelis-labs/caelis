//go:build !windows

package file

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
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
	err = waitForOwnerLock(ctx, func() (bool, error) {
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
	if err := validateOpenedRegular(root, path, file); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	return &unixOwnerLock{file: file}, nil
}

type unixOwnerLock struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *unixOwnerLock) Close() error {
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
