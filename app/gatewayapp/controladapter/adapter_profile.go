package controladapter

import "context"

func (d *Adapter) AgentProfileStatus(ctx context.Context) (AgentProfileStatusSnapshot, error) {
	return d.stack.AgentProfileStatus(ctx)
}

func (d *Adapter) BindAgentProfile(ctx context.Context, cfg AgentProfileBindingConfig) (AgentProfileStatusSnapshot, error) {
	return d.stack.BindAgentProfile(ctx, cfg)
}
