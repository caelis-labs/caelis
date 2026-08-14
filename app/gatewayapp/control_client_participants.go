package gatewayapp

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

// ParticipantHandles projects the directly runnable handles from the fixed
// Session Runtime configuration. Reading an idle Session uses current app
// configuration but deliberately does not activate or retain a Runtime.
func (s *Stack) ParticipantHandles(ctx context.Context, sessionID string) ([]string, error) {
	if s == nil {
		return nil, errors.New("gatewayapp: stack is unavailable")
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

	runtimeStack := s
	var release func()
	if s.sessionRuntimes != nil {
		// Serialize the loaded check with activation and configuration mutation.
		// If another command is assembling this Session, wait until it publishes
		// the fixed Runtime rather than projecting a newer Host catalog.
		activationCtx, unlock, err := s.sessionRuntimes.lockActivation(ctx)
		if err != nil {
			return nil, err
		}
		defer unlock()
		ctx = activationCtx
		runtime, releaseUse, err := s.sessionRuntimes.acquireLoadedRuntime(sessionID)
		if err != nil {
			return nil, err
		}
		release = releaseUse
		if runtime != nil {
			runtimeStack = runtime.stack
		}
	}
	if release != nil {
		defer release()
	}

	snapshot, err := runtimeStack.placementSnapshot(ctx)
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

var _ controlclient.ParticipantHandleReader = (*Stack)(nil)
