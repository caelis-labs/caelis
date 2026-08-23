package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/internal/hostownership"
)

const (
	productHostOwnershipFilename = hostownership.Filename
	productHostOwnershipTimeout  = 200 * time.Millisecond
)

// ErrProductHostOwnershipConflict reports that another product Host process
// already owns this store directory. Presentation clients must attach to that
// Host instead of starting a second product Host.
var ErrProductHostOwnershipConflict = errors.New("cli: another product Control Host already owns this store directory")

// acquireProductHostOwnership takes the exclusive product Host ownership guard
// for one store directory. Call only at product Host process entry (caelis serve
// or embedded, whether explicit or a missing-service fallback) before opening shared Host state. Do not call from
// NewLocalStack or generic Runtime assembly.
func acquireProductHostOwnership(storeDir string) (*hostownership.Authority, error) {
	path := productHostOwnershipPath(storeDir)
	if path == "" {
		return nil, errors.New("cli: product Host ownership store directory is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), productHostOwnershipTimeout)
	defer cancel()
	authority, err := hostownership.Acquire(ctx, storeDir)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("%w: %s", ErrProductHostOwnershipConflict, path)
		}
		return nil, fmt.Errorf("cli: acquire product Host ownership: %w", err)
	}
	return authority, nil
}

func productHostOwnershipPath(storeDir string) string {
	return hostownership.Path(storeDir)
}
