package gatewayapp

import (
	"context"
	"errors"
)

// sessionRuntimeInstance is one activated Session execution composition. It
// borrows the focused process authorities recorded in runtimeComposition but
// owns only its pinned configuration and disposable execution resources.
// Host-only clients, registries, operation stores, and lifecycle cancellation
// never enter this type.
type sessionRuntimeInstance struct {
	runtimeComposition
}

// Quiesce permanently closes this Session Runtime's Turn admission and waits
// for its Gateway and child controller work. Process-wide Registry draining is
// owned by Stack and is deliberately absent here.
func (r *sessionRuntimeInstance) Quiesce(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.closing.Store(true)
	var gatewayErr error
	if gateway := r.currentGateway(); gateway != nil {
		gatewayErr = gateway.Quiesce(ctx)
	}
	var childErr error
	if r.acpControlPlane != nil {
		childErr = r.acpControlPlane.Quiesce(ctx)
	}
	return errors.Join(gatewayErr, childErr)
}
