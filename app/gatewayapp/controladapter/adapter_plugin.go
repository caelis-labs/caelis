package controladapter

import "context"

func (d *Adapter) ListPlugins(ctx context.Context) ([]PluginSnapshot, error) {
	return d.stack.ListPlugins(ctx)
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
