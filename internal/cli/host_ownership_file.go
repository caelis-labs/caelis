package cli

import (
	"context"
	"io"

	"github.com/caelis-labs/caelis/internal/filelock"
)

func acquireProductHostOwnershipFile(ctx context.Context, path string) (io.Closer, error) {
	return filelock.Acquire(ctx, path)
}
