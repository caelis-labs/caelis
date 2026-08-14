package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/internal/productpaths"
)

const (
	productHostOwnershipFilename = "authority.lock"
	productHostOwnershipTimeout  = 200 * time.Millisecond
)

// ErrProductHostOwnershipConflict reports that another product Host process
// already owns this store directory. Presentation clients must attach to that
// Host instead of starting a second product Host.
var ErrProductHostOwnershipConflict = errors.New("cli: another product Control Host already owns this store directory")

// acquireProductHostOwnership takes the exclusive product Host ownership guard
// for one store directory. Call only at product Host process entry (caelis serve
// or explicit --embedded) before opening shared Host state. Do not call from
// NewLocalStack or generic Runtime assembly.
func acquireProductHostOwnership(storeDir string) (io.Closer, error) {
	path := productHostOwnershipPath(storeDir)
	if path == "" {
		return nil, errors.New("cli: product Host ownership store directory is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), productHostOwnershipTimeout)
	defer cancel()
	lease, err := acquireProductHostOwnershipFile(ctx, path)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("%w: %s", ErrProductHostOwnershipConflict, path)
		}
		return nil, fmt.Errorf("cli: acquire product Host ownership: %w", err)
	}
	return lease, nil
}

func productHostOwnershipPath(storeDir string) string {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return ""
	}
	return filepath.Join(productpaths.ServiceRuntimeDir(storeDir), productHostOwnershipFilename)
}
