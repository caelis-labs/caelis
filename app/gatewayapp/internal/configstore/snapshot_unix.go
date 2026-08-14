//go:build !windows

package configstore

import (
	"context"
	"os"
)

func readAppConfigSnapshot(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
