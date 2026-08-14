//go:build !windows

package gatewayapp

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

func acquireACPPreparationFileLock(ctx context.Context, path string) (io.Closer, error) {
	file, err := openACPPreparationLockFile(path)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return acpPreparationUnixFileLock{file: file}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type acpPreparationUnixFileLock struct {
	file *os.File
}

func (l acpPreparationUnixFileLock) Close() error {
	if l.file == nil {
		return nil
	}
	return errors.Join(syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN), l.file.Close())
}
