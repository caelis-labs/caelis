package windows

import (
	"fmt"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

type Config = sandbox.Config

type backendFactory struct{}
type legacyBackendFactory struct{}

func (backendFactory) Backend() sandbox.Backend { return sandbox.BackendWindows }
func (legacyBackendFactory) Backend() sandbox.Backend {
	return sandbox.BackendWindowsElevated
}

func (backendFactory) Build(cfg sandbox.Config) (sandbox.Runtime, error) {
	return New(cfg)
}
func (legacyBackendFactory) Build(cfg sandbox.Config) (sandbox.Runtime, error) {
	cfg.RequestedBackend = sandbox.BackendWindows
	return New(cfg)
}

func New(cfg Config) (sandbox.Runtime, error) {
	if cfg.ResourceLimits != nil {
		return nil, fmt.Errorf("sandbox: backend cannot enforce explicit resource limits")
	}
	return newRuntime(sandbox.NormalizeConfig(cfg))
}

func init() {
	sandbox.RegisterBuiltInBackendFactory(backendFactory{})
	sandbox.RegisterBuiltInBackendFactory(legacyBackendFactory{})
}
