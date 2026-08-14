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

func acquireManagedACPFileLock(ctx context.Context, path string, onWait func()) (io.Closer, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	waiting := false
	overlapped := &windows.Overlapped{}
	for {
		err = windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, overlapped,
		)
		if err == nil {
			return managedACPWindowsFileLock{file: file, overlapped: overlapped}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = file.Close()
			return nil, err
		}
		if !waiting {
			waiting = true
			if onWait != nil {
				onWait()
			}
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type managedACPWindowsFileLock struct {
	file       *os.File
	overlapped *windows.Overlapped
}

func (l managedACPWindowsFileLock) Close() error {
	if l.file == nil {
		return nil
	}
	unlockErr := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, l.overlapped)
	return errors.Join(unlockErr, l.file.Close())
}
