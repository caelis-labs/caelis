package controladapter

import (
	"context"

	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (d *assembler) ListPlugins(ctx context.Context) ([]controlprompt.PluginSnapshot, error) {
	if d.deps.Plugin.ListPluginsFn == nil {
		return nil, missingRuntimeDependency("list plugins")
	}
	return d.deps.Plugin.ListPluginsFn(ctx)
}

func (d *assembler) ListMarketplaces(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
	if d.deps.Plugin.ListMarketplacesFn == nil {
		return nil, missingRuntimeDependency("list marketplaces")
	}
	return d.deps.Plugin.ListMarketplacesFn(ctx)
}

func (d *assembler) InspectPlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	if d.deps.Plugin.InspectPluginFn == nil {
		return controlprompt.PluginSnapshot{}, missingRuntimeDependency("inspect plugin")
	}
	return d.deps.Plugin.InspectPluginFn(ctx, id)
}
