package seatbelt

import (
	"context"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/sandbox/host"
)

// Backend runs commands through macOS sandbox-exec seatbelt profiles.
type Backend struct {
	host *host.Backend
}

// New creates a seatbelt backend.
func New() *Backend {
	return &Backend{host: host.New()}
}

func (b *Backend) Name() string { return "seatbelt" }

func (b *Backend) Describe(context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{
		Name:        "seatbelt",
		Description: "macOS sandbox-exec seatbelt execution",
		Platform:    "darwin",
		Features:    []string{"command", "filesystem", "path-policy"},
	}, nil
}

func (b *Backend) FileSystem(ctx context.Context, c sandbox.Constraints) (sandbox.FileSystem, error) {
	return b.host.FileSystem(ctx, c)
}

func (b *Backend) Close() error { return b.host.Close() }

// Compile-time interface check.
var _ sandbox.Backend = (*Backend)(nil)
