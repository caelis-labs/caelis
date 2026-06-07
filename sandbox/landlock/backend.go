package landlock

import (
	"context"

	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/sandbox/host"
)

// Backend is the Layer 4 Landlock sandbox backend.
type Backend struct {
	host *host.Backend
}

// New creates a Landlock backend.
func New() *Backend {
	return &Backend{host: host.New()}
}

func (b *Backend) Name() string { return "landlock" }

func (b *Backend) Describe(context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{
		Name:        "landlock",
		Description: "Linux Landlock filesystem restrictions",
		Platform:    "linux",
		Features:    []string{"command", "filesystem", "path-policy"},
	}, nil
}

func (b *Backend) FileSystem(ctx context.Context, c sandbox.Constraints) (sandbox.FileSystem, error) {
	return b.host.FileSystem(ctx, c)
}

func (b *Backend) Close() error { return b.host.Close() }

var _ sandbox.Backend = (*Backend)(nil)
