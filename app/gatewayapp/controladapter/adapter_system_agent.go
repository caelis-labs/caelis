package controladapter

import (
	"context"

	controlsystemagent "github.com/caelis-labs/caelis/control/systemagent"
)

// SystemAgentStatus returns fixed Control-managed Agents and model targets.
func (d *Adapter) SystemAgentStatus(ctx context.Context) (controlsystemagent.Status, error) {
	if d == nil || d.stack == nil || d.stack.SystemAgent.StatusFn == nil {
		return controlsystemagent.Status{}, missingRuntimeDependency("system Agent status")
	}
	return d.stack.SystemAgent.StatusFn(delegationContext(ctx))
}

// BindSystemAgent binds one fixed system Agent to a model-backed roster Agent.
func (d *Adapter) BindSystemAgent(ctx context.Context, req controlsystemagent.BindRequest) (controlsystemagent.Status, error) {
	if d == nil || d.stack == nil || d.stack.SystemAgent.BindFn == nil {
		return controlsystemagent.Status{}, missingRuntimeDependency("system Agent bind")
	}
	return d.stack.SystemAgent.BindFn(delegationContext(ctx), req)
}

// ResetSystemAgent restores one system Agent to its default model behavior.
func (d *Adapter) ResetSystemAgent(ctx context.Context, id controlsystemagent.ID) (controlsystemagent.Status, error) {
	if d == nil || d.stack == nil || d.stack.SystemAgent.ResetFn == nil {
		return controlsystemagent.Status{}, missingRuntimeDependency("system Agent reset")
	}
	return d.stack.SystemAgent.ResetFn(delegationContext(ctx), id)
}

var _ controlsystemagent.Service = (*Adapter)(nil)
