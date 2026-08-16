package gatewayapp

import (
	"context"
	"errors"
)

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
