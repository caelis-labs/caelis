//go:build windows

package configstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

func readAppConfigSnapshot(ctx context.Context, path string) (data []byte, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_SEQUENTIAL_SCAN,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: errors.New("invalid file handle")}
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close AppConfig snapshot: %w", closeErr))
		}
	}()
	data, err = io.ReadAll(file)
	if err != nil {
		return nil, &os.PathError{Op: "read", Path: path, Err: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}
