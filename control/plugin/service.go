package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// ErrHostUnavailable reports use of a Service that has no application host.
var ErrHostUnavailable = errors.New("plugin service: host is unavailable")

// Service owns Plugin discovery, lifecycle, installation, marketplace
// operations, and the Control Plugin Info view while delegating the product
// data root, persistence, Runtime replacement, and live MCP probing to its host.
type Service struct {
	host Host
}

// NewService creates the Control Plugin service over one application host.
func NewService(host Host) Service {
	return Service{host: host}
}

func (s Service) requireHost() (Host, error) {
	if s.host == nil {
		return nil, ErrHostUnavailable
	}
	return s.host, nil
}

func (s Service) loadState(ctx context.Context) (State, error) {
	host, err := s.requireHost()
	if err != nil {
		return State{}, err
	}
	return host.LoadPluginState(ctx)
}

func (s Service) updateState(ctx context.Context, mutation Mutation) error {
	host, err := s.requireHost()
	if err != nil {
		return err
	}
	return host.UpdatePluginState(ctx, mutation)
}

func (s Service) storeDirectory() (string, error) {
	host, err := s.requireHost()
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(host.StoreDir())
	if dir == "" {
		return "", fmt.Errorf("plugin service: host store directory is unavailable")
	}
	return dir, nil
}

// Info is the current Control view of one configured plugin.
type Info struct {
	ID          string
	Name        string
	Version     string
	Description string
	Root        string
	Enabled     bool
	Skills      []string
	Hooks       []string
	Agents      []string
	MCPServers  []mcp.MCPServerInfo
	Status      string
	Warning     string
}

// CacheCleanupError reports that a plugin was removed from configuration and
// Runtime successfully, but its managed install cache could not be deleted.
// Retrying Remove is not appropriate because the plugin is no longer
// configured; callers may report or separately clean up the cache.
type CacheCleanupError struct {
	PluginID string
	Err      error
}

func (e *CacheCleanupError) Error() string {
	if e == nil {
		return "plugin service: plugin removed but managed cache cleanup failed"
	}
	return fmt.Sprintf("plugin service: plugin %q removed but managed cache cleanup failed: %v", e.PluginID, e.Err)
}

// Unwrap exposes the cache deletion failure.
func (e *CacheCleanupError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type pluginAddPathOptions struct {
	Managed    bool
	CacheRoot  string
	ConfigID   string
	UpsertMode pluginInstallUpsertMode
}

type pluginInstallUpsertMode int

const (
	pluginInstallUpsertByID pluginInstallUpsertMode = iota
	pluginInstallUpsertByRoot
)

func (s Service) List(ctx context.Context) ([]Info, error) {
	doc, err := s.loadState(ctx)
	if err != nil {
		return nil, err
	}

	var list []Info
	for _, pCfg := range doc.Plugins {
		info := s.pluginInfoFromConfig(pCfg)

		// For disabled plugins skip deep parse – show stored metadata only.
		if !pCfg.Enabled {
			list = append(list, info)
			continue
		}

		s.enrichPluginInfoFromManifest(&info, pCfg)
		list = append(list, info)
	}

	return list, nil
}

// Install installs one plugin from a Claude Code compatible source. Local
// directories are registered directly. plugin@marketplace resolves a Claude
// marketplace.json and then registers the referenced plugin directory.
func (s Service) Install(ctx context.Context, source string) (Info, error) {
	if _, err := s.requireHost(); err != nil {
		return Info{}, err
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return Info{}, fmt.Errorf("plugin service: plugin install source is required")
	}
	if info, ok, err := s.installLocalPluginSource(ctx, source); ok || err != nil {
		return info, err
	}
	pluginName, marketplaceRef, ok := strings.Cut(source, "@")
	if !ok || strings.TrimSpace(pluginName) == "" || strings.TrimSpace(marketplaceRef) == "" {
		return Info{}, fmt.Errorf("plugin service: expected plugin@marketplace or local plugin directory")
	}
	marketplaceRoot, err := s.resolveMarketplaceRoot(ctx, marketplaceRef)
	if err != nil {
		return Info{}, err
	}
	manifest, err := readPluginMarketplaceManifest(marketplaceRoot)
	if err != nil {
		return Info{}, err
	}
	entry, ok := findMarketplacePlugin(manifest, pluginName)
	if !ok {
		return Info{}, fmt.Errorf("plugin service: plugin %q not found in marketplace %q", strings.TrimSpace(pluginName), strings.TrimSpace(marketplaceRef))
	}
	pluginRoot, err := s.resolveMarketplacePluginRoot(ctx, marketplaceRoot, manifest, entry)
	if err != nil {
		return Info{}, err
	}
	storeDir, err := s.storeDirectory()
	if err != nil {
		return Info{}, err
	}
	cacheRoot := managedPluginInstallCacheRoot(storeDir, pluginRoot)
	return s.addPath(ctx, pluginRoot, pluginAddPathOptions{
		Managed:    cacheRoot != "",
		CacheRoot:  cacheRoot,
		ConfigID:   entry.Name,
		UpsertMode: pluginInstallUpsertByRoot,
	})
}

func (s Service) installLocalPluginSource(ctx context.Context, source string) (Info, bool, error) {
	absPath, err := filepath.Abs(strings.TrimSpace(source))
	if err != nil {
		return Info{}, false, nil
	}
	fi, err := os.Stat(absPath)
	if err != nil || !fi.IsDir() {
		return Info{}, false, nil
	}
	info, err := s.AddPath(ctx, absPath)
	return info, true, err
}

// AddPath registers or updates a local plugin directory, enables it, and
// rebuilds the gateway. If the gateway rebuild fails the config is rolled back.
func (s Service) AddPath(ctx context.Context, path string) (Info, error) {
	if _, err := s.requireHost(); err != nil {
		return Info{}, err
	}
	return s.addPath(ctx, path, pluginAddPathOptions{})
}

func (s Service) addPath(ctx context.Context, path string, opts pluginAddPathOptions) (Info, error) {
	absPath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return Info{}, fmt.Errorf("plugin service: failed to resolve absolute path: %w", err)
	}
	fi, err := os.Stat(absPath)
	if err != nil {
		return Info{}, fmt.Errorf("plugin service: path does not exist: %w", err)
	}
	if !fi.IsDir() {
		return Info{}, fmt.Errorf("plugin service: path is not a directory: %s", absPath)
	}

	id := pluginConfigID(absPath, opts.ConfigID)
	p, err := ParseConfigured(Config{
		ID:   id,
		Root: absPath,
	})
	if err != nil {
		return Info{}, fmt.Errorf("plugin service: parse plugin failed: %w", err)
	}

	next := Config{
		ID:          id,
		Name:        p.Name,
		Root:        absPath,
		Manifest:    p.Manifest,
		Kind:        p.Kind,
		Enabled:     true,
		Version:     p.Version,
		Description: p.Description,
		Managed:     opts.Managed,
		CacheRoot:   strings.TrimSpace(opts.CacheRoot),
	}
	var info Info
	if err := s.updateState(ctx, Mutation{
		GuardAction:   "add plugin",
		FailureAction: "add plugin",
		Reconfigure:   true,
		Apply: func(state *State) error {
			switch opts.UpsertMode {
			case pluginInstallUpsertByRoot:
				return upsertMarketplacePluginConfig(state, next)
			default:
				upsertLocalPluginConfig(state, next)
				return nil
			}
		},
		AfterCommit: func(state State) error {
			var err error
			info, err = s.inspectState(state, id)
			return err
		},
	}); err != nil {
		return Info{}, err
	}

	return info, nil
}

// Enable marks a registered plugin as enabled and requests Runtime
// reconfiguration. If host reconfiguration fails, the config is rolled back.
func (s Service) Enable(ctx context.Context, id string) (Info, error) {
	return s.setEnabled(ctx, id, true)
}

// Disable marks a registered plugin as disabled and requests Runtime
// reconfiguration. If host reconfiguration fails, the config is rolled back.
func (s Service) Disable(ctx context.Context, id string) (Info, error) {
	return s.setEnabled(ctx, id, false)
}

func (s Service) setEnabled(ctx context.Context, id string, enabled bool) (Info, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	action := "disable plugin"
	if enabled {
		action = "enable plugin"
	}

	var info Info
	if err := s.updateState(ctx, Mutation{
		GuardAction:   action,
		FailureAction: action,
		Reconfigure:   true,
		Apply: func(state *State) error {
			foundIdx := pluginConfigIndexByID(state.Plugins, id)
			if foundIdx < 0 {
				return fmt.Errorf("plugin service: plugin not found: %s", id)
			}
			state.Plugins[foundIdx].Enabled = enabled
			return nil
		},
		AfterCommit: func(state State) error {
			var err error
			info, err = s.inspectState(state, id)
			return err
		},
	}); err != nil {
		return Info{}, err
	}

	return info, nil
}

// Remove removes a plugin from the registry and requests Runtime
// reconfiguration. If host reconfiguration fails, the config is rolled back.
func (s Service) Remove(ctx context.Context, id string) error {
	id = strings.ToLower(strings.TrimSpace(id))

	var removed Config
	if err := s.updateState(ctx, Mutation{
		GuardAction:   "remove plugin",
		FailureAction: "remove plugin",
		Reconfigure:   true,
		Apply: func(state *State) error {
			foundIdx := pluginConfigIndexByID(state.Plugins, id)
			if foundIdx < 0 {
				return fmt.Errorf("plugin service: plugin not found: %s", id)
			}
			removed = state.Plugins[foundIdx]
			state.Plugins = append(state.Plugins[:foundIdx], state.Plugins[foundIdx+1:]...)
			return nil
		},
		AfterCommit: func(State) error {
			if err := s.removeManagedPluginCache(removed); err != nil {
				return &CacheCleanupError{PluginID: removed.ID, Err: err}
			}
			return nil
		},
	}); err != nil {
		return err
	}
	return nil
}

func (s Service) Inspect(ctx context.Context, id string) (Info, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	doc, err := s.loadState(ctx)
	if err != nil {
		return Info{}, err
	}
	return s.inspectState(doc, id)
}

func (s Service) inspectState(doc State, id string) (Info, error) {
	foundIdx := pluginConfigIndexByID(doc.Plugins, id)
	if foundIdx < 0 {
		return Info{}, fmt.Errorf("plugin service: plugin not found: %s", id)
	}

	pCfg := doc.Plugins[foundIdx]
	info := s.pluginInfoFromConfig(pCfg)
	s.enrichPluginInfoFromManifest(&info, pCfg)

	return info, nil
}

func (s Service) pluginInfoFromConfig(pCfg Config) Info {
	info := Info{
		ID:          pCfg.ID,
		Name:        pCfg.Name,
		Version:     pCfg.Version,
		Description: pCfg.Description,
		Root:        pCfg.Root,
		Enabled:     pCfg.Enabled,
		Status:      "inactive",
	}
	if pCfg.Enabled {
		info.Status = "active"
		if host, err := s.requireHost(); err == nil {
			info.MCPServers = host.MCPServersStatus(pCfg.ID)
		}
	}
	return info
}

func (s Service) enrichPluginInfoFromManifest(info *Info, pCfg Config) {
	if info == nil {
		return
	}
	p, err := ParseConfigured(pCfg)
	if err != nil {
		info.Status = "error"
		info.Warning = err.Error()
		return
	}
	info.Name = firstNonEmpty(info.Name, p.Name)
	info.Version = firstNonEmpty(info.Version, p.Version)
	info.Description = firstNonEmpty(info.Description, p.Description)
	info.Skills = pluginSkillDisplayNames(p)
	for _, hook := range p.Hooks {
		info.Hooks = append(info.Hooks, string(hook.Event))
	}
	for _, agent := range p.Agents {
		if name := strings.TrimSpace(agent.Name); name != "" {
			info.Agents = append(info.Agents, name)
		}
	}
	for _, mcpSpec := range p.MCPServers {
		if pluginInfoHasMCPServer(*info, mcpSpec.Name) {
			continue
		}
		status := "stopped"
		if !pCfg.Enabled {
			status = "disabled"
		}
		info.MCPServers = append(info.MCPServers, mcp.MCPServerInfo{
			Name:   mcpSpec.Name,
			Status: status,
		})
	}
}

func pluginSkillDisplayNames(p InstalledPlugin) []string {
	bundles := pluginSkillBundles(p, true)
	if len(bundles) == 0 {
		return nil
	}
	metas, err := skillfs.DiscoverPluginBundleMeta(bundles)
	if err != nil {
		out := make([]string, 0, len(p.Skills))
		for _, sc := range p.Skills {
			if root := strings.TrimSpace(sc.Root); root != "" {
				out = append(out, filepath.Base(root))
			}
		}
		return out
	}
	out := make([]string, 0, len(metas))
	for _, meta := range metas {
		if name := strings.TrimSpace(meta.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func pluginSkillBundles(p InstalledPlugin, enabled bool) []skill.PluginBundle {
	if len(p.Skills) == 0 {
		return nil
	}
	out := make([]skill.PluginBundle, 0, len(p.Skills))
	for _, contribution := range p.Skills {
		if strings.TrimSpace(contribution.Root) == "" {
			continue
		}
		out = append(out, skill.PluginBundle{
			Plugin:    p.ID,
			Namespace: contribution.Namespace,
			Root:      contribution.Root,
			Disabled:  append([]string(nil), contribution.Disabled...),
			Enabled:   enabled,
		})
	}
	return out
}

func pluginInfoHasMCPServer(info Info, name string) bool {
	for _, live := range info.MCPServers {
		if live.Name == name {
			return true
		}
	}
	return false
}

func (s Service) removeManagedPluginCache(cfg Config) error {
	if !cfg.Managed {
		return nil
	}
	cacheRoot := strings.TrimSpace(cfg.CacheRoot)
	storeDir, err := s.storeDirectory()
	if err != nil {
		return err
	}
	if cacheRoot == "" {
		cacheRoot = managedPluginInstallCacheRoot(storeDir, cfg.Root)
	}
	if cacheRoot == "" {
		return nil
	}
	installedRoot, err := filepath.Abs(filepath.Join(storeDir, "plugins", "installed"))
	if err != nil {
		return err
	}
	cacheRoot, err = filepath.Abs(cacheRoot)
	if err != nil {
		return err
	}
	if !PathWithinRoot(installedRoot, cacheRoot) || filepath.Clean(installedRoot) == filepath.Clean(cacheRoot) {
		return fmt.Errorf("plugin service: refusing to remove unmanaged plugin cache path: %s", cacheRoot)
	}
	return os.RemoveAll(cacheRoot)
}

func managedPluginInstallCacheRoot(storeDir string, pluginRoot string) string {
	storeDir = strings.TrimSpace(storeDir)
	pluginRoot = strings.TrimSpace(pluginRoot)
	if storeDir == "" || pluginRoot == "" {
		return ""
	}
	installedRoot, err := filepath.Abs(filepath.Join(storeDir, "plugins", "installed"))
	if err != nil {
		return ""
	}
	pluginRoot, err = filepath.Abs(pluginRoot)
	if err != nil {
		return ""
	}
	if !PathWithinRoot(installedRoot, pluginRoot) || filepath.Clean(installedRoot) == filepath.Clean(pluginRoot) {
		return ""
	}
	rel, err := filepath.Rel(installedRoot, pluginRoot)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return ""
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" || parts[0] == "." || parts[0] == ".." {
		return ""
	}
	cacheRoot := filepath.Join(installedRoot, parts[0])
	if !PathWithinRoot(cacheRoot, pluginRoot) {
		return ""
	}
	return cacheRoot
}
