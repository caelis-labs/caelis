package landlock

import sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"

type Config = sdksandbox.Config

type backendFactory struct{}

func (backendFactory) Backend() sdksandbox.Backend { return sdksandbox.BackendLandlock }

func (backendFactory) Build(cfg sdksandbox.Config) (sdksandbox.Runtime, error) {
	return New(cfg)
}

func New(cfg Config) (sdksandbox.Runtime, error) {
	return newRuntime(cfg)
}

func init() {
	sdksandbox.RegisterBuiltInBackendFactory(backendFactory{})
}
