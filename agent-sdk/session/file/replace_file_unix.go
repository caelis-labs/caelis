//go:build !windows

package file

import (
	"context"
	"log/slog"
	"os"
)

func replaceFile(_ context.Context, _ *slog.Logger, _ fileOperation, from, to string) error {
	return os.Rename(from, to)
}

func removeFile(_ context.Context, _ *slog.Logger, _ fileOperation, path string) error {
	return os.Remove(path)
}
