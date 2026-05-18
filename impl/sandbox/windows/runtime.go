package windows

import "github.com/OnslaughtSnail/caelis/ports/sandbox"

type Config = sandbox.Config

type backendFactory struct{}

func (backendFactory) Backend() sandbox.Backend { return sandbox.BackendWindowsElevated }

func (backendFactory) Build(cfg sandbox.Config) (sandbox.Runtime, error) {
	return New(cfg)
}

func New(cfg Config) (sandbox.Runtime, error) {
	return newRuntime(sandbox.NormalizeConfig(cfg))
}

func init() {
	sandbox.RegisterBuiltInBackendFactory(backendFactory{})
}
