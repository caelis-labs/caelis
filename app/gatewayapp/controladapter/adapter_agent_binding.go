package controladapter

import (
	"context"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

// AgentBindingStatus returns the Control-owned Agent configuration view.
func (d *Adapter) AgentBindingStatus(ctx context.Context) (agentbinding.Status, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.Configuration == nil {
		return agentbinding.Status{}, missingRuntimeDependency("Agent binding status")
	}
	return d.stack.AgentBinding.Configuration.AgentBindingStatus(bindingContext(ctx))
}

// BindAgentBinding binds one handle to a ModelProfile and effort.
func (d *Adapter) BindAgentBinding(ctx context.Context, binding agentbinding.Binding) (agentbinding.Status, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.Configuration == nil {
		return agentbinding.Status{}, missingRuntimeDependency("Agent binding mutation")
	}
	return d.stack.AgentBinding.Configuration.BindAgentBinding(bindingContext(ctx), binding)
}

// ResetAgentBinding removes one handle's explicit profile binding.
func (d *Adapter) ResetAgentBinding(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.Configuration == nil {
		return agentbinding.Status{}, missingRuntimeDependency("Agent binding reset")
	}
	return d.stack.AgentBinding.Configuration.ResetAgentBinding(bindingContext(ctx), handle)
}

// CreateAgentRole adds one custom delegation role.
func (d *Adapter) CreateAgentRole(
	ctx context.Context,
	role agentbinding.Role,
	initial agentbinding.Binding,
) (agentbinding.Status, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.Configuration == nil {
		return agentbinding.Status{}, missingRuntimeDependency("Agent role creation")
	}
	return d.stack.AgentBinding.Configuration.CreateAgentRole(bindingContext(ctx), role, initial)
}

// DeleteAgentRole removes one custom delegation role.
func (d *Adapter) DeleteAgentRole(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.Configuration == nil {
		return agentbinding.Status{}, missingRuntimeDependency("Agent role deletion")
	}
	return d.stack.AgentBinding.Configuration.DeleteAgentRole(bindingContext(ctx), handle)
}

// SaveAgentBindingSet snapshots the active bindings under name.
func (d *Adapter) SaveAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.Configuration == nil {
		return agentbinding.Status{}, missingRuntimeDependency("Agent binding-set save")
	}
	return d.stack.AgentBinding.Configuration.SaveAgentBindingSet(bindingContext(ctx), name)
}

// ApplyAgentBindingSet atomically activates one saved binding snapshot.
func (d *Adapter) ApplyAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.Configuration == nil {
		return agentbinding.Status{}, missingRuntimeDependency("Agent binding-set apply")
	}
	return d.stack.AgentBinding.Configuration.ApplyAgentBindingSet(bindingContext(ctx), name)
}

// DeleteAgentBindingSet removes one saved binding snapshot.
func (d *Adapter) DeleteAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	if d == nil || d.stack == nil || d.stack.AgentBinding.Configuration == nil {
		return agentbinding.Status{}, missingRuntimeDependency("Agent binding-set deletion")
	}
	return d.stack.AgentBinding.Configuration.DeleteAgentBindingSet(bindingContext(ctx), name)
}

func bindingContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ agentbinding.ConfigurationService = (*Adapter)(nil)
