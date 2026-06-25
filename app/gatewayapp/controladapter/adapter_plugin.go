package controladapter

import "context"

func (d *Adapter) ListPlugins(ctx context.Context) ([]PluginSnapshot, error) {
	if d.stack.Plugin.ListPluginsFn == nil {
		return nil, missingRuntimeDependency("list plugins")
	}
	return d.stack.Plugin.ListPluginsFn(ctx)
}

func (d *Adapter) AddMarketplace(ctx context.Context, source string) (MarketplaceSnapshot, error) {
	if d.stack.Plugin.AddMarketplaceFn == nil {
		return MarketplaceSnapshot{}, missingRuntimeDependency("add marketplace")
	}
	return d.stack.Plugin.AddMarketplaceFn(ctx, source)
}

func (d *Adapter) ListMarketplaces(ctx context.Context) ([]MarketplaceSnapshot, error) {
	if d.stack.Plugin.ListMarketplacesFn == nil {
		return nil, missingRuntimeDependency("list marketplaces")
	}
	return d.stack.Plugin.ListMarketplacesFn(ctx)
}

func (d *Adapter) UpdateMarketplace(ctx context.Context, name string) (MarketplaceSnapshot, error) {
	if d.stack.Plugin.UpdateMarketplaceFn == nil {
		return MarketplaceSnapshot{}, missingRuntimeDependency("update marketplace")
	}
	return d.stack.Plugin.UpdateMarketplaceFn(ctx, name)
}

func (d *Adapter) RemoveMarketplace(ctx context.Context, name string) error {
	if d.stack.Plugin.RemoveMarketplaceFn == nil {
		return missingRuntimeDependency("remove marketplace")
	}
	return d.stack.Plugin.RemoveMarketplaceFn(ctx, name)
}

func (d *Adapter) AddPluginPath(ctx context.Context, path string) (PluginSnapshot, error) {
	if d.stack.Plugin.AddPluginPathFn == nil {
		return PluginSnapshot{}, missingRuntimeDependency("add plugin path")
	}
	return d.stack.Plugin.AddPluginPathFn(ctx, path)
}

func (d *Adapter) InstallPlugin(ctx context.Context, source string) (PluginSnapshot, error) {
	if d.stack.Plugin.InstallPluginFn == nil {
		return PluginSnapshot{}, missingRuntimeDependency("install plugin")
	}
	return d.stack.Plugin.InstallPluginFn(ctx, source)
}

func (d *Adapter) EnablePlugin(ctx context.Context, id string) (PluginSnapshot, error) {
	if d.stack.Plugin.EnablePluginFn == nil {
		return PluginSnapshot{}, missingRuntimeDependency("enable plugin")
	}
	return d.stack.Plugin.EnablePluginFn(ctx, id)
}

func (d *Adapter) DisablePlugin(ctx context.Context, id string) (PluginSnapshot, error) {
	if d.stack.Plugin.DisablePluginFn == nil {
		return PluginSnapshot{}, missingRuntimeDependency("disable plugin")
	}
	return d.stack.Plugin.DisablePluginFn(ctx, id)
}

func (d *Adapter) RemovePlugin(ctx context.Context, id string) error {
	if d.stack.Plugin.RemovePluginFn == nil {
		return missingRuntimeDependency("remove plugin")
	}
	return d.stack.Plugin.RemovePluginFn(ctx, id)
}

func (d *Adapter) InspectPlugin(ctx context.Context, id string) (PluginSnapshot, error) {
	if d.stack.Plugin.InspectPluginFn == nil {
		return PluginSnapshot{}, missingRuntimeDependency("inspect plugin")
	}
	return d.stack.Plugin.InspectPluginFn(ctx, id)
}
