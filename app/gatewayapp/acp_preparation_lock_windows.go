//go:build windows

package gatewayapp

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func acquireACPPreparationFileLock(ctx context.Context, path string) (io.Closer, error) {
	file, err := openACPPreparationLockFile(path)
	if err != nil {
		return nil, err
	}
	overlapped := &windows.Overlapped{}
	for {
		err = windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			overlapped,
		)
		if err == nil {
			return acpPreparationWindowsFileLock{file: file, overlapped: overlapped}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
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

type acpPreparationWindowsFileLock struct {
	file       *os.File
	overlapped *windows.Overlapped
}

func (l acpPreparationWindowsFileLock) Close() error {
	if l.file == nil {
		return nil
	}
	unlockErr := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, l.overlapped)
	return errors.Join(unlockErr, l.file.Close())
}
