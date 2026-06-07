package bwrap

import (
	"context"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/sandbox/host"
)

// Backend runs commands through Linux bubblewrap.
type Backend struct {
	host *host.Backend
}

// New creates a bubblewrap backend.
func New() *Backend {
	return &Backend{host: host.New()}
}

func (b *Backend) Name() string { return "bwrap" }

func (b *Backend) Describe(context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{
		Name:        "bwrap",
		Description: "Linux bubblewrap execution",
		Platform:    "linux",
		Features:    []string{"command", "filesystem", "path-policy"},
	}, nil
}

func (b *Backend) FileSystem(ctx context.Context, c sandbox.Constraints) (sandbox.FileSystem, error) {
	return b.host.FileSystem(ctx, c)
}

func (b *Backend) Close() error { return b.host.Close() }

var _ sandbox.Backend = (*Backend)(nil)
