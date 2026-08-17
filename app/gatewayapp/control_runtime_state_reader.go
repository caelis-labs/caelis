package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// controlRuntimeStateReader is the focused live-state dependency supplied to
// the Control reconnect service. It is constructed before the Session Runtime
// registry and bound once after registry assembly, without retaining Stack.
type controlRuntimeStateReader struct {
	defaultRuntime *runtimeComposition

	mu       sync.RWMutex
	registry *sessionRuntimeRegistry
}

func newControlRuntimeStateReader(defaultRuntime *runtimeComposition) (*controlRuntimeStateReader, error) {
	if defaultRuntime == nil {
		return nil, errors.New("gatewayapp: default control runtime is required")
	}
	return &controlRuntimeStateReader{defaultRuntime: defaultRuntime}, nil
}

func (r *controlRuntimeStateReader) bindRegistry(registry *sessionRuntimeRegistry) error {
	if r == nil || registry == nil {
		return errors.New("gatewayapp: control runtime registry is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.registry != nil && r.registry != registry {
		return errors.New("gatewayapp: control runtime registry is already bound")
	}
	r.registry = registry
	return nil
}

// ControlClientRuntimeState reads live state only from an already activated
// Session Runtime. Observation never assembles or retains execution state.
func (r *controlRuntimeStateReader) ControlClientRuntimeState(ctx context.Context, ref session.SessionRef) (appserver.RuntimeState, error) {
	if r == nil || r.defaultRuntime == nil {
		return appserver.RuntimeState{}, fmt.Errorf("gatewayapp: control runtime is unavailable")
	}
	r.mu.RLock()
	registry := r.registry
	r.mu.RUnlock()
	composition := r.defaultRuntime
	if registry != nil {
		runtime, ok := registry.loaded(ref.SessionID)
		if !ok {
			return appserver.RuntimeState{}, nil
		}
		composition = &runtime.instance.runtimeComposition
	}
	gateway := composition.currentGateway()
	if gateway == nil {
		return appserver.RuntimeState{}, fmt.Errorf("gatewayapp: control runtime is unavailable")
	}
	return gateway.ControlClientRuntimeState(ctx, ref)
}

var _ appserver.RuntimeStateReader = (*controlRuntimeStateReader)(nil)
