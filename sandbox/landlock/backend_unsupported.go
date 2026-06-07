//go:build !linux

package landlock

import (
	"context"
	"fmt"
	"runtime"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

func (b *Backend) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, fmt.Errorf("landlock sandbox is only supported on linux (current=%s)", runtime.GOOS)
}

func (b *Backend) Status(context.Context) (sandbox.Status, error) {
	return sandbox.Status{Running: false}, nil
}
