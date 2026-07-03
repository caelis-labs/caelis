package landlock

import "github.com/caelis-labs/caelis/ports/sandbox"

type Config = sandbox.Config

type backendFactory struct{}

func (backendFactory) Backend() sandbox.Backend { return sandbox.BackendLandlock }

func (backendFactory) Build(cfg sandbox.Config) (sandbox.Runtime, error) {
	return New(cfg)
}

func New(cfg Config) (sandbox.Runtime, error) {
	return newRuntime(cfg)
}

func init() {
	sandbox.RegisterBuiltInBackendFactory(backendFactory{})
}
