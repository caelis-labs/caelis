package controladapter

import (
	"context"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

// AgentBindingStatus returns the Control-owned fixed-handle view.
func (d *Adapter) AgentBindingStatus(ctx context.Context) (agentbinding.Status, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.StatusFn == nil {
		return agentbinding.Status{}, missingRuntimeDependency("Agent binding status")
	}
	return d.stack.AgentBinding.StatusFn(bindingContext(ctx))
}

// BindAgentBinding binds one fixed handle to a ModelProfile and effort.
func (d *Adapter) BindAgentBinding(ctx context.Context, binding agentbinding.Binding) (agentbinding.Status, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.BindFn == nil {
		return agentbinding.Status{}, missingRuntimeDependency("Agent binding mutation")
	}
	return d.stack.AgentBinding.BindFn(bindingContext(ctx), binding)
}

// ResetAgentBinding removes one fixed handle's explicit profile binding.
func (d *Adapter) ResetAgentBinding(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.ResetFn == nil {
		return agentbinding.Status{}, missingRuntimeDependency("Agent binding reset")
	}
	return d.stack.AgentBinding.ResetFn(bindingContext(ctx), handle)
}

func bindingContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ agentbinding.Service = (*Adapter)(nil)
