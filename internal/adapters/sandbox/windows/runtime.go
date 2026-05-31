package windows

import (
	"context"

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
	return newRuntime(sandbox.NormalizeConfig(cfg))
}

var _ sandbox.BackendFactory = Factory{}
