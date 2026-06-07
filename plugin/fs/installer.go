package fs

import (
	"context"
	"fmt"

	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
)

type InstallerConfig struct {
	Resolver caelisplugin.Resolver
	Store    caelisplugin.Store
}

type Installer struct {
	resolver caelisplugin.Resolver
	store    caelisplugin.Store
}

// NewInstaller returns a filesystem-backed plugin installer.
func NewInstaller(cfg InstallerConfig) *Installer {
	resolver := cfg.Resolver
	if resolver == nil {
		resolver = NewResolver()
	}
	return &Installer{resolver: resolver, store: cfg.Store}
}

func (i *Installer) Install(ctx context.Context, req caelisplugin.InstallRequest) (caelisplugin.Installed, error) {
	if i == nil || i.store == nil {
		return caelisplugin.Installed{}, fmt.Errorf("plugin/fs: installer store is required")
	}
	resolved, err := i.resolver.Resolve(ctx, caelisplugin.ResolveRequest{Root: req.Root})
	if err != nil {
		return caelisplugin.Installed{}, err
	}
	return i.store.Install(ctx, resolved)
}
