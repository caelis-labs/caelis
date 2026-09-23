package gatewayapp

import (
	"errors"
	"sync"
	"sync/atomic"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// controlCommandBackend is the Host-private execution authority behind the
// principal-bound AppServer command service. It owns only command-scoped Host
// state and a once-bound Session Runtime registry; it never retains the
// composition-root Stack.
type controlCommandBackend struct {
	composition   *runtimeComposition
	modelRecovery *sessionModelRecovery

	sessionRuntimes atomic.Pointer[sessionRuntimeRegistry]

	acpPreparations *acpPreparationStore

	hostAuthenticationMu sync.Mutex
	hostAuthentications  map[string]struct{}

	// Optional test seam; nil uses the platform lifecycle runtime factory.
	sandboxLifecycleFactory sandboxLifecycleRuntimeFactory
}

func newControlCommandBackend(composition *runtimeComposition, modelRecovery *sessionModelRecovery) (*controlCommandBackend, error) {
	if composition == nil || modelRecovery == nil {
		return nil, errors.New("gatewayapp: control command backend dependencies are required")
	}
	return &controlCommandBackend{
		composition:         composition,
		modelRecovery:       modelRecovery,
		hostAuthentications: map[string]struct{}{},
	}, nil
}

func (b *controlCommandBackend) bindSessionRuntimes(registry *sessionRuntimeRegistry) error {
	if b == nil || registry == nil {
		return errors.New("gatewayapp: control command Runtime registry is required")
	}
	if b.sessionRuntimes.CompareAndSwap(nil, registry) || b.sessionRuntimes.Load() == registry {
		return nil
	}
	return errors.New("gatewayapp: control command Runtime registry is already bound")
}

func (b *controlCommandBackend) runtimeRegistry() *sessionRuntimeRegistry {
	if b == nil {
		return nil
	}
	return b.sessionRuntimes.Load()
}

var _ appserver.CommandBackend = (*controlCommandBackend)(nil)
var _ appserver.CommandRecoveryBackend = (*controlCommandBackend)(nil)
