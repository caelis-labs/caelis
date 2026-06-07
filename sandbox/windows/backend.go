package windows

import (
	"context"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/sandbox/host"
)

// Backend is the Layer 4 Windows workspace-write sandbox backend.
type Backend struct {
	host *host.Backend
}

// New creates a Windows sandbox backend.
func New() *Backend {
	return &Backend{host: host.New()}
}

func (b *Backend) Name() string { return "windows" }

func (b *Backend) Describe(context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{
		Name:        "windows",
		Description: "Windows restricted-token workspace sandbox",
		Platform:    "windows",
		Features:    []string{"command", "filesystem", "path-policy"},
	}, nil
}

func (b *Backend) FileSystem(ctx context.Context, c sandbox.Constraints) (sandbox.FileSystem, error) {
	return b.host.FileSystem(ctx, c)
}

func (b *Backend) Close() error { return b.host.Close() }

var _ sandbox.Backend = (*Backend)(nil)
