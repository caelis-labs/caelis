package controladapter

import "context"

func (d *Adapter) AgentProfileStatus(ctx context.Context) (AgentProfileStatusSnapshot, error) {
	if d.stack.AgentProfile.StatusFn == nil {
		return AgentProfileStatusSnapshot{}, missingRuntimeDependency("agent profile")
	}
	return d.stack.AgentProfile.StatusFn(ctx)
}

func (d *Adapter) BindAgentProfile(ctx context.Context, cfg AgentProfileBindingConfig) (AgentProfileStatusSnapshot, error) {
	if d.stack.AgentProfile.BindFn == nil {
		return AgentProfileStatusSnapshot{}, missingRuntimeDependency("agent profile binding")
	}
	return d.stack.AgentProfile.BindFn(ctx, cfg)
}
