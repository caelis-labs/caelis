package controladapter

import (
	"context"

	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (d *assembler) ListPlugins(ctx context.Context) ([]controlprompt.PluginSnapshot, error) {
	if d.stack.Plugin.ListPluginsFn == nil {
		return nil, missingRuntimeDependency("list plugins")
	}
	return d.stack.Plugin.ListPluginsFn(ctx)
}

func (d *assembler) ListMarketplaces(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
	if d.stack.Plugin.ListMarketplacesFn == nil {
		return nil, missingRuntimeDependency("list marketplaces")
	}
	return d.stack.Plugin.ListMarketplacesFn(ctx)
}

func (d *assembler) InspectPlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	if d.stack.Plugin.InspectPluginFn == nil {
		return controlprompt.PluginSnapshot{}, missingRuntimeDependency("inspect plugin")
	}
	return d.stack.Plugin.InspectPluginFn(ctx, id)
}
