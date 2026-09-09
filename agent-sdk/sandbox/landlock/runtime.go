package landlock

import (
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

type Config = sandbox.Config

type backendFactory struct{}

func (backendFactory) Backend() sandbox.Backend { return sandbox.BackendLandlock }

func (backendFactory) Build(cfg sandbox.Config) (sandbox.Runtime, error) {
	return New(cfg)
}

func New(cfg Config) (sandbox.Runtime, error) {
	if cfg.ResourceLimits != nil {
		return nil, fmt.Errorf("sandbox: backend cannot enforce explicit resource limits")
	}
	return newRuntime(cfg)
}

func init() {
	sandbox.RegisterBuiltInBackendFactory(backendFactory{})
}
