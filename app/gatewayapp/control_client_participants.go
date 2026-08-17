package gatewayapp

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/control/agentbinding"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// participantHandleReader is the focused Runtime projection supplied to the
// Control participant service. It is constructed before the Session Runtime
// registry and bound once after registry assembly, without retaining Stack.
type participantHandleReader struct {
	defaultRuntime *runtimeComposition

	mu       sync.RWMutex
	registry *sessionRuntimeRegistry
}

func newParticipantHandleReader(defaultRuntime *runtimeComposition) (*participantHandleReader, error) {
	if defaultRuntime == nil {
		return nil, errors.New("gatewayapp: default participant runtime is required")
	}
	return &participantHandleReader{defaultRuntime: defaultRuntime}, nil
}

func (r *participantHandleReader) bindRegistry(registry *sessionRuntimeRegistry) error {
	if r == nil || registry == nil {
		return errors.New("gatewayapp: participant Runtime registry is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.registry != nil && r.registry != registry {
		return errors.New("gatewayapp: participant Runtime registry is already bound")
	}
	r.registry = registry
	return nil
}

// ParticipantHandles projects directly runnable handles from the fixed
// Session Runtime configuration. Reading an idle Session uses current app
// configuration but deliberately does not activate or retain a Runtime.
func (r *participantHandleReader) ParticipantHandles(ctx context.Context, sessionID string) ([]string, error) {
	if r == nil || r.defaultRuntime == nil {
		return nil, errors.New("gatewayapp: participant Runtime is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("gatewayapp: Session ID is required")
	}

	composition := r.defaultRuntime
	var release func()
	r.mu.RLock()
	registry := r.registry
	r.mu.RUnlock()
	if registry != nil {
		// Serialize the loaded check with activation and configuration mutation.
		// If another command is assembling this Session, wait until it publishes
		// the fixed Runtime rather than projecting a newer Host catalog.
		activationCtx, unlock, err := registry.lockActivation(ctx)
		if err != nil {
			return nil, err
		}
		defer unlock()
		ctx = activationCtx
		runtime, releaseUse, err := registry.acquireLoadedRuntime(sessionID)
		if err != nil {
			return nil, err
		}
		release = releaseUse
		if runtime != nil {
			composition = &runtime.instance.runtimeComposition
		}
	}
	if release != nil {
		defer release()
	}

	snapshot, err := composition.placementSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	status := agentBindingStatusFromConfig(snapshot.placement.Bindings, snapshot.placement.Profiles)
	bound := agentbinding.BoundDirectHandles(status)
	handles := make([]string, 0, len(bound))
	for _, item := range bound {
		if handle := agentbinding.NormalizeHandle(item.Definition.Handle); handle != "" {
			handles = append(handles, string(handle))
		}
	}
	return handles, nil
}

var _ appserver.ParticipantHandleReader = (*participantHandleReader)(nil)
