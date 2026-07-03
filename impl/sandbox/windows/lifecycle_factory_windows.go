//go:build windows

package windows

import "github.com/caelis-labs/caelis/ports/sandbox"

type lifecycleFactory struct{}

func (lifecycleFactory) Backend() sandbox.Backend { return sandbox.BackendWindows }

func (lifecycleFactory) BuildLifecycle(cfg sandbox.Config) (sandbox.Runtime, error) {
	cfg.RequestedBackend = sandbox.BackendWindows
	return New(cfg)
}

func init() {
	sandbox.RegisterBuiltInLifecycleFactory(lifecycleFactory{})
}
