package windows

import "github.com/OnslaughtSnail/caelis/ports/sandbox"

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
	return newRuntime(sandbox.NormalizeConfig(cfg))
}

func init() {
	sandbox.RegisterBuiltInBackendFactory(backendFactory{})
	sandbox.RegisterBuiltInBackendFactory(legacyBackendFactory{})
}
