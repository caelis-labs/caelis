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

func (d *assembler) AddMarketplace(ctx context.Context, source string) (controlprompt.MarketplaceSnapshot, error) {
	if d.stack.Plugin.AddMarketplaceFn == nil {
		return controlprompt.MarketplaceSnapshot{}, missingRuntimeDependency("add marketplace")
	}
	return d.stack.Plugin.AddMarketplaceFn(ctx, source)
}

func (d *assembler) ListMarketplaces(ctx context.Context) ([]controlprompt.MarketplaceSnapshot, error) {
	if d.stack.Plugin.ListMarketplacesFn == nil {
		return nil, missingRuntimeDependency("list marketplaces")
	}
	return d.stack.Plugin.ListMarketplacesFn(ctx)
}

func (d *assembler) UpdateMarketplace(ctx context.Context, name string) (controlprompt.MarketplaceSnapshot, error) {
	if d.stack.Plugin.UpdateMarketplaceFn == nil {
		return controlprompt.MarketplaceSnapshot{}, missingRuntimeDependency("update marketplace")
	}
	return d.stack.Plugin.UpdateMarketplaceFn(ctx, name)
}

func (d *assembler) RemoveMarketplace(ctx context.Context, name string) error {
	if d.stack.Plugin.RemoveMarketplaceFn == nil {
		return missingRuntimeDependency("remove marketplace")
	}
	return d.stack.Plugin.RemoveMarketplaceFn(ctx, name)
}

func (d *assembler) AddPluginPath(ctx context.Context, path string) (controlprompt.PluginSnapshot, error) {
	if d.stack.Plugin.AddPluginPathFn == nil {
		return controlprompt.PluginSnapshot{}, missingRuntimeDependency("add plugin path")
	}
	return d.stack.Plugin.AddPluginPathFn(ctx, path)
}

func (d *assembler) InstallPlugin(ctx context.Context, source string) (controlprompt.PluginSnapshot, error) {
	if d.stack.Plugin.InstallPluginFn == nil {
		return controlprompt.PluginSnapshot{}, missingRuntimeDependency("install plugin")
	}
	return d.stack.Plugin.InstallPluginFn(ctx, source)
}

func (d *assembler) EnablePlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	if d.stack.Plugin.EnablePluginFn == nil {
		return controlprompt.PluginSnapshot{}, missingRuntimeDependency("enable plugin")
	}
	return d.stack.Plugin.EnablePluginFn(ctx, id)
}

func (d *assembler) DisablePlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	if d.stack.Plugin.DisablePluginFn == nil {
		return controlprompt.PluginSnapshot{}, missingRuntimeDependency("disable plugin")
	}
	return d.stack.Plugin.DisablePluginFn(ctx, id)
}

func (d *assembler) RemovePlugin(ctx context.Context, id string) error {
	if d.stack.Plugin.RemovePluginFn == nil {
		return missingRuntimeDependency("remove plugin")
	}
	return d.stack.Plugin.RemovePluginFn(ctx, id)
}

func (d *assembler) InspectPlugin(ctx context.Context, id string) (controlprompt.PluginSnapshot, error) {
	if d.stack.Plugin.InspectPluginFn == nil {
		return controlprompt.PluginSnapshot{}, missingRuntimeDependency("inspect plugin")
	}
	return d.stack.Plugin.InspectPluginFn(ctx, id)
}
