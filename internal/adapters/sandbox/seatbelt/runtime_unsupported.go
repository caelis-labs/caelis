//go:build !darwin

package seatbelt

import (
	"context"
	"fmt"
	"runtime"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
)

type Config = sandbox.Config

type Factory struct{}

func (Factory) NewRuntime(ctx context.Context, cfg sandbox.Config) (sandbox.Runtime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return New(cfg)
}

func New(cfg Config) (sandbox.Runtime, error) {
	_ = cfg
	return nil, fmt.Errorf("seatbelt sandbox is only supported on darwin (current=%s)", runtime.GOOS)
}

var _ sandbox.BackendFactory = Factory{}
