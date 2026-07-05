//go:build !linux

package bwrap

import (
	"fmt"
	"runtime"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

type Config = sandbox.Config

type backendFactory struct{}

func (backendFactory) Backend() sandbox.Backend { return sandbox.BackendBwrap }

func (backendFactory) Build(cfg sandbox.Config) (sandbox.Runtime, error) {
	return New(cfg)
}

func New(cfg Config) (sandbox.Runtime, error) {
	_ = cfg
	return nil, fmt.Errorf("bwrap sandbox is only supported on linux (current=%s)", runtime.GOOS)
}

func init() {
	sandbox.RegisterBuiltInBackendFactory(backendFactory{})
}
