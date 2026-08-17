package controladapter

import (
	"context"

	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// pluginAssembler retains only pure plugin read capabilities. It cannot
// accidentally grow access to Session, model, status, or gateway authorities.
type pluginAssembler struct {
	deps PluginRuntimeDeps
}

func (d *pluginAssembler) ListPlugins(ctx context.Context) ([]controlprompt.PluginSnapshot, error) {
	if d == nil || d.deps.ListPluginsFn == nil {
		return nil, missingRuntimeDependency("list plugins")
	}
	return d.deps.ListPluginsFn(ctx)
}

func (d *pluginAssembler) ListMarketplaces(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
	if d == nil || d.deps.ListMarketplacesFn == nil {
		return nil, missingRuntimeDependency("list marketplaces")
	}
	return d.deps.ListMarketplacesFn(ctx)
}

func (d *pluginAssembler) InspectPlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	if d == nil || d.deps.InspectPluginFn == nil {
		return controlprompt.PluginSnapshot{}, missingRuntimeDependency("inspect plugin")
	}
	return d.deps.InspectPluginFn(ctx, id)
}

var _ PluginAssembler = (*pluginAssembler)(nil)
