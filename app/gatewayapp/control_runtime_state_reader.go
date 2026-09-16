package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
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

// ControlClientRuntimeState reads an activated Runtime or recovers the last
// canonical outcome through the Host Runtime's existing journal reader.
// Observation never assembles or retains a Session Runtime.
func (r *controlRuntimeStateReader) ControlClientRuntimeState(ctx context.Context, ref session.SessionRef) (appserver.RuntimeState, error) {
	if r == nil || r.defaultRuntime == nil {
		return appserver.RuntimeState{}, fmt.Errorf("gatewayapp: control runtime is unavailable")
	}
	r.mu.RLock()
	registry := r.registry
	r.mu.RUnlock()
	composition := r.defaultRuntime
	dormant := false
	if registry != nil {
		if runtime, ok := registry.loaded(ref.SessionID); ok {
			composition = &runtime.instance.runtimeComposition
		} else {
			dormant = true
		}
	}
	gateway := composition.currentGateway()
	if gateway == nil {
		return appserver.RuntimeState{}, fmt.Errorf("gatewayapp: control runtime is unavailable")
	}
	state, err := gateway.ControlClientRuntimeState(ctx, ref)
	if err != nil || !dormant {
		return state, err
	}
	// Prefer an activation published while canonical history was being read.
	if runtime, ok := registry.loaded(ref.SessionID); ok {
		return runtime.instance.currentGateway().ControlClientRuntimeState(ctx, ref)
	}
	if !state.Run.Active {
		switch agent.RunLifecycleStatus(state.Run.Status) {
		case agent.RunLifecycleStatusRunning, agent.RunLifecycleStatusWaitingApproval:
			// No current producer and no durable terminal fact: do not claim
			// success, cancellation, or an actionable approval after a crash.
			state.Run.Status = eventstream.LifecycleStateUnknown
			state.Run.WaitingApproval = false
		}
	}
	return state, nil
}

var _ appserver.RuntimeStateReader = (*controlRuntimeStateReader)(nil)
