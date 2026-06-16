package controladapter

import "context"

func (d *Adapter) ListPlugins(ctx context.Context) ([]PluginSnapshot, error) {
	return d.stack.ListPlugins(ctx)
}

func (d *Adapter) AddMarketplace(ctx context.Context, source string) (MarketplaceSnapshot, error) {
	return d.stack.AddMarketplace(ctx, source)
}

func (d *Adapter) ListMarketplaces(ctx context.Context) ([]MarketplaceSnapshot, error) {
	return d.stack.ListMarketplaces(ctx)
}

func (d *Adapter) UpdateMarketplace(ctx context.Context, name string) (MarketplaceSnapshot, error) {
	return d.stack.UpdateMarketplace(ctx, name)
}

func (d *Adapter) RemoveMarketplace(ctx context.Context, name string) error {
	return d.stack.RemoveMarketplace(ctx, name)
}

func (d *Adapter) AddPluginPath(ctx context.Context, path string) (PluginSnapshot, error) {
	return d.stack.AddPluginPath(ctx, path)
}

func (d *Adapter) InstallPlugin(ctx context.Context, source string) (PluginSnapshot, error) {
	return d.stack.InstallPlugin(ctx, source)
}

func (d *Adapter) EnablePlugin(ctx context.Context, id string) (PluginSnapshot, error) {
	return d.stack.EnablePlugin(ctx, id)
}

func (d *Adapter) DisablePlugin(ctx context.Context, id string) (PluginSnapshot, error) {
	return d.stack.DisablePlugin(ctx, id)
}

func (d *Adapter) RemovePlugin(ctx context.Context, id string) error {
	return d.stack.RemovePlugin(ctx, id)
}

func (d *Adapter) InspectPlugin(ctx context.Context, id string) (PluginSnapshot, error) {
	return d.stack.InspectPlugin(ctx, id)
}
