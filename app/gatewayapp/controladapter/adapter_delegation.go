package controladapter

import (
	"context"

	controldelegation "github.com/caelis-labs/caelis/control/delegation"
)

// DelegationStatus returns the Control-owned fixed delegation profile view.
func (d *Adapter) DelegationStatus(ctx context.Context) (controldelegation.Status, error) {
	if d == nil || d.stack == nil || d.stack.Delegation.StatusFn == nil {
		return controldelegation.Status{}, missingRuntimeDependency("delegation status")
	}
	return d.stack.Delegation.StatusFn(delegationContext(ctx))
}

// BindDelegation binds one configurable profile to a roster Agent.
func (d *Adapter) BindDelegation(ctx context.Context, req controldelegation.BindRequest) (controldelegation.Status, error) {
	if d == nil || d.stack == nil || d.stack.Delegation.BindFn == nil {
		return controldelegation.Status{}, missingRuntimeDependency("delegation bind")
	}
	return d.stack.Delegation.BindFn(delegationContext(ctx), req)
}

// ResetDelegation removes one configurable profile's explicit Agent binding.
func (d *Adapter) ResetDelegation(ctx context.Context, profile controldelegation.Profile) (controldelegation.Status, error) {
	if d == nil || d.stack == nil || d.stack.Delegation.ResetFn == nil {
		return controldelegation.Status{}, missingRuntimeDependency("delegation reset")
	}
	return d.stack.Delegation.ResetFn(delegationContext(ctx), profile)
}

func delegationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ controldelegation.Service = (*Adapter)(nil)
