//go:build linux

package landlock

import (
	"context"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

func (b *Backend) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return b.host.Run(ctx, req)
}

func (b *Backend) Status(context.Context) (sandbox.Status, error) {
	return sandbox.Status{Running: true}, nil
}
