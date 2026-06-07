package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
)

const lockFileName = "plugins.lock.json"

type StoreConfig struct {
	Root string
}

type Store struct {
	root string
}

// NewStore returns a filesystem-backed installed plugin store.
func NewStore(cfg StoreConfig) *Store {
	return &Store{root: strings.TrimSpace(cfg.Root)}
}

func (s *Store) Install(_ context.Context, resolved caelisplugin.Resolved) (caelisplugin.Installed, error) {
	if s == nil || s.root == "" {
		return caelisplugin.Installed{}, fmt.Errorf("plugin/fs: store root is required")
	}
	if strings.TrimSpace(resolved.Manifest.Name) == "" {
		return caelisplugin.Installed{}, fmt.Errorf("plugin/fs: plugin name is required")
	}
	if strings.TrimSpace(resolved.Root) == "" {
		return caelisplugin.Installed{}, fmt.Errorf("plugin/fs: resolved root is required")
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return caelisplugin.Installed{}, err
	}
	lock, err := s.readLock()
	if err != nil {
		return caelisplugin.Installed{}, err
	}
	installed := caelisplugin.Installed{
		Name:            strings.TrimSpace(resolved.Manifest.Name),
		Version:         strings.TrimSpace(resolved.Manifest.Version),
		Root:            filepath.Clean(resolved.Root),
		Skills:          append([]caelisplugin.SkillBundle(nil), resolved.Manifest.Contributions.Skills...),
		MCPServers:      append([]caelisplugin.MCPServer(nil), resolved.Runtime.MCPServers...),
		Agents:          append([]caelisplugin.AgentConfig(nil), resolved.Runtime.Agents...),
		Modes:           append([]caelisplugin.ModeConfig(nil), resolved.Runtime.Modes...),
		Configs:         append([]caelisplugin.ConfigOption(nil), resolved.Runtime.Configs...),
		SystemPrompt:    strings.TrimSpace(resolved.Runtime.SystemPrompt),
		PolicyMode:      strings.TrimSpace(resolved.Runtime.PolicyMode),
		ExtraReadRoots:  append([]string(nil), resolved.Runtime.ExtraReadRoots...),
		ExtraWriteRoots: append([]string(nil), resolved.Runtime.ExtraWriteRoots...),
	}
	if len(installed.MCPServers) == 0 {
		installed.MCPServers = append([]caelisplugin.MCPServer(nil), resolved.Manifest.Contributions.MCPServers...)
	}
	if len(installed.Agents) == 0 {
		installed.Agents = append([]caelisplugin.AgentConfig(nil), resolved.Manifest.Contributions.Agents...)
	}
	if len(installed.Modes) == 0 {
		installed.Modes = append([]caelisplugin.ModeConfig(nil), resolved.Manifest.Contributions.Modes...)
	}
	if len(installed.Configs) == 0 {
		installed.Configs = append([]caelisplugin.ConfigOption(nil), resolved.Manifest.Contributions.Configs...)
	}
	if installed.SystemPrompt == "" {
		installed.SystemPrompt = strings.TrimSpace(resolved.Manifest.Contributions.SystemPrompt)
	}
	if installed.PolicyMode == "" {
		installed.PolicyMode = strings.TrimSpace(resolved.Manifest.Contributions.PolicyMode)
	}
	if len(installed.ExtraReadRoots) == 0 {
		installed.ExtraReadRoots = append([]string(nil), resolved.Manifest.Contributions.ExtraReadRoots...)
	}
	if len(installed.ExtraWriteRoots) == 0 {
		installed.ExtraWriteRoots = append([]string(nil), resolved.Manifest.Contributions.ExtraWriteRoots...)
	}
	next := lockFile{Version: 1}
	for _, one := range lock.Plugins {
		if one.Name != installed.Name {
			next.Plugins = append(next.Plugins, one)
		}
	}
	next.Plugins = append(next.Plugins, installed)
	if err := s.writeLock(next); err != nil {
		return caelisplugin.Installed{}, err
	}
	return installed, nil
}

func (s *Store) List(_ context.Context) ([]caelisplugin.Installed, error) {
	lock, err := s.readLock()
	if err != nil {
		return nil, err
	}
	return append([]caelisplugin.Installed(nil), lock.Plugins...), nil
}

func (s *Store) Load(_ context.Context, name string) (caelisplugin.Installed, error) {
	name = strings.TrimSpace(name)
	lock, err := s.readLock()
	if err != nil {
		return caelisplugin.Installed{}, err
	}
	for _, one := range lock.Plugins {
		if one.Name == name {
			return one, nil
		}
	}
	return caelisplugin.Installed{}, fmt.Errorf("plugin/fs: plugin %q not installed", name)
}

type lockFile struct {
	Version int                      `json:"version"`
	Plugins []caelisplugin.Installed `json:"plugins,omitempty"`
}

func (s *Store) readLock() (lockFile, error) {
	if s == nil || s.root == "" {
		return lockFile{Version: 1}, nil
	}
	data, err := os.ReadFile(filepath.Join(s.root, lockFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return lockFile{Version: 1}, nil
		}
		return lockFile{}, err
	}
	var lock lockFile
	if err := json.Unmarshal(data, &lock); err != nil {
		return lockFile{}, err
	}
	if lock.Version == 0 {
		lock.Version = 1
	}
	return lock, nil
}

func (s *Store) writeLock(lock lockFile) error {
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.root, lockFileName), append(data, '\n'), 0o600)
}

type RegistryConfig struct {
	Store    caelisplugin.Store
	Resolver caelisplugin.Resolver
}

type Registry struct {
	store    caelisplugin.Store
	resolver caelisplugin.Resolver
}

// NewRegistry returns a registry over installed plugin records.
func NewRegistry(cfg RegistryConfig) *Registry {
	resolver := cfg.Resolver
	if resolver == nil {
		resolver = NewResolver()
	}
	return &Registry{store: cfg.Store, resolver: resolver}
}

func (r *Registry) List(ctx context.Context) ([]caelisplugin.Resolved, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	installed, err := r.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]caelisplugin.Resolved, 0, len(installed))
	for _, one := range installed {
		resolved, err := r.resolver.Resolve(ctx, caelisplugin.ResolveRequest{Root: one.Root})
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

func (r *Registry) Load(ctx context.Context, name string) (caelisplugin.Resolved, error) {
	if r == nil || r.store == nil {
		return caelisplugin.Resolved{}, fmt.Errorf("plugin/fs: registry store is required")
	}
	installed, err := r.store.Load(ctx, name)
	if err != nil {
		return caelisplugin.Resolved{}, err
	}
	return r.resolver.Resolve(ctx, caelisplugin.ResolveRequest{Root: installed.Root})
}
