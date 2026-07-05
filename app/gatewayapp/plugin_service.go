package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/pluginregistry"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/promptassembly"
	pluginapi "github.com/caelis-labs/caelis/ports/plugin"
)

type PluginService struct {
	stack *Stack
}

func (s *Stack) Plugins() PluginService {
	return PluginService{stack: s}
}

type PluginInfo struct {
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

func (s PluginService) List(ctx context.Context) ([]PluginInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return nil, fmt.Errorf("plugin service: stack store is unavailable")
	}
	doc, err := s.stack.store.Load()
	if err != nil {
		return nil, err
	}

	var list []PluginInfo
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
func (s PluginService) Install(ctx context.Context, source string) (PluginInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return PluginInfo{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return PluginInfo{}, fmt.Errorf("plugin service: plugin install source is required")
	}
	if info, ok, err := s.installLocalPluginSource(ctx, source); ok || err != nil {
		return info, err
	}
	pluginName, marketplaceRef, ok := strings.Cut(source, "@")
	if !ok || strings.TrimSpace(pluginName) == "" || strings.TrimSpace(marketplaceRef) == "" {
		return PluginInfo{}, fmt.Errorf("plugin service: expected plugin@marketplace or local plugin directory")
	}
	marketplaceRoot, err := s.resolveMarketplaceRoot(ctx, marketplaceRef)
	if err != nil {
		return PluginInfo{}, err
	}
	manifest, err := readPluginMarketplaceManifest(marketplaceRoot)
	if err != nil {
		return PluginInfo{}, err
	}
	entry, ok := findMarketplacePlugin(manifest, pluginName)
	if !ok {
		return PluginInfo{}, fmt.Errorf("plugin service: plugin %q not found in marketplace %q", strings.TrimSpace(pluginName), strings.TrimSpace(marketplaceRef))
	}
	pluginRoot, err := s.resolveMarketplacePluginRoot(ctx, marketplaceRoot, manifest, entry)
	if err != nil {
		return PluginInfo{}, err
	}
	cacheRoot := managedPluginInstallCacheRoot(s.stack.storeDir, pluginRoot)
	return s.addPath(ctx, pluginRoot, pluginAddPathOptions{
		Managed:    cacheRoot != "",
		CacheRoot:  cacheRoot,
		ConfigID:   entry.Name,
		UpsertMode: pluginInstallUpsertByRoot,
	})
}

func (s PluginService) installLocalPluginSource(ctx context.Context, source string) (PluginInfo, bool, error) {
	absPath, err := filepath.Abs(strings.TrimSpace(source))
	if err != nil {
		return PluginInfo{}, false, nil
	}
	fi, err := os.Stat(absPath)
	if err != nil || !fi.IsDir() {
		return PluginInfo{}, false, nil
	}
	info, err := s.AddPath(ctx, absPath)
	return info, true, err
}

// AddPath registers or updates a local plugin directory, enables it, and
// rebuilds the gateway. If the gateway rebuild fails the config is rolled back.
func (s PluginService) AddPath(ctx context.Context, path string) (PluginInfo, error) {
	return s.addPath(ctx, path, pluginAddPathOptions{})
}

func (s PluginService) addPath(ctx context.Context, path string, opts pluginAddPathOptions) (PluginInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return PluginInfo{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	absPath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return PluginInfo{}, fmt.Errorf("plugin service: failed to resolve absolute path: %w", err)
	}
	fi, err := os.Stat(absPath)
	if err != nil {
		return PluginInfo{}, fmt.Errorf("plugin service: path does not exist: %w", err)
	}
	if !fi.IsDir() {
		return PluginInfo{}, fmt.Errorf("plugin service: path is not a directory: %s", absPath)
	}

	id := pluginConfigID(absPath, opts.ConfigID)
	p, err := parseConfiguredPlugin(PluginConfig{
		ID:   id,
		Root: absPath,
	})
	if err != nil {
		return PluginInfo{}, fmt.Errorf("plugin service: parse plugin failed: %w", err)
	}

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("add plugin"); err != nil {
		return PluginInfo{}, err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return PluginInfo{}, err
	}
	doc := cloneAppConfig(oldDoc)

	next := PluginConfig{
		ID:          id,
		Name:        p.Name,
		Root:        absPath,
		Manifest:    p.Manifest,
		Kind:        string(p.Kind),
		Enabled:     true,
		Version:     p.Version,
		Description: p.Description,
		Managed:     opts.Managed,
		CacheRoot:   strings.TrimSpace(opts.CacheRoot),
	}
	var upsertErr error
	switch opts.UpsertMode {
	case pluginInstallUpsertByRoot:
		upsertErr = upsertMarketplacePluginConfig(&doc, next)
	default:
		upsertLocalPluginConfig(&doc, next)
	}
	if upsertErr != nil {
		return PluginInfo{}, upsertErr
	}

	if err := s.stack.store.Save(doc); err != nil {
		return PluginInfo{}, err
	}

	if err := s.stack.rebuildGateway(); err != nil {
		return PluginInfo{}, s.handleRebuildError("rebuild gateway after adding plugin", err, oldDoc)
	}

	return s.Inspect(ctx, id)
}

// Enable marks a registered plugin as enabled and rebuilds the gateway.
// If the rebuild fails the config is rolled back.
func (s PluginService) Enable(ctx context.Context, id string) (PluginInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return PluginInfo{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	id = strings.ToLower(strings.TrimSpace(id))

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("enable plugin"); err != nil {
		return PluginInfo{}, err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return PluginInfo{}, err
	}
	doc := cloneAppConfig(oldDoc)

	foundIdx, err := findPluginConfigIndex(doc, id)
	if err != nil {
		return PluginInfo{}, err
	}

	doc.Plugins[foundIdx].Enabled = true
	if err := s.stack.store.Save(doc); err != nil {
		return PluginInfo{}, err
	}

	if err := s.stack.rebuildGateway(); err != nil {
		return PluginInfo{}, s.handleRebuildError("enable plugin", err, oldDoc)
	}

	return s.Inspect(ctx, id)
}

// Disable marks a registered plugin as disabled and rebuilds the gateway.
// If the rebuild fails the config is rolled back.
func (s PluginService) Disable(ctx context.Context, id string) (PluginInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return PluginInfo{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	id = strings.ToLower(strings.TrimSpace(id))

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("disable plugin"); err != nil {
		return PluginInfo{}, err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return PluginInfo{}, err
	}
	doc := cloneAppConfig(oldDoc)

	foundIdx, err := findPluginConfigIndex(doc, id)
	if err != nil {
		return PluginInfo{}, err
	}

	doc.Plugins[foundIdx].Enabled = false
	if err := s.stack.store.Save(doc); err != nil {
		return PluginInfo{}, err
	}

	if err := s.stack.rebuildGateway(); err != nil {
		return PluginInfo{}, s.handleRebuildError("disable plugin", err, oldDoc)
	}

	return s.Inspect(ctx, id)
}

// Remove removes a plugin from the registry and rebuilds the gateway.
// If the rebuild fails the config is rolled back.
func (s PluginService) Remove(ctx context.Context, id string) error {
	if s.stack == nil || s.stack.store == nil {
		return fmt.Errorf("plugin service: stack store is unavailable")
	}
	id = strings.ToLower(strings.TrimSpace(id))

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("remove plugin"); err != nil {
		return err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return err
	}
	doc := cloneAppConfig(oldDoc)

	foundIdx, err := findPluginConfigIndex(doc, id)
	if err != nil {
		return err
	}
	removed := doc.Plugins[foundIdx]

	doc.Plugins = append(doc.Plugins[:foundIdx], doc.Plugins[foundIdx+1:]...)
	if err := s.stack.store.Save(doc); err != nil {
		return err
	}

	if err := s.stack.rebuildGateway(); err != nil {
		return s.handleRebuildError("rebuild gateway after removing plugin", err, oldDoc)
	}

	if err := s.removeManagedPluginCache(removed); err != nil {
		return err
	}

	return nil
}

func (s PluginService) Inspect(ctx context.Context, id string) (PluginInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return PluginInfo{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	id = strings.ToLower(strings.TrimSpace(id))
	doc, err := s.stack.store.Load()
	if err != nil {
		return PluginInfo{}, err
	}

	foundIdx, err := findPluginConfigIndex(doc, id)
	if err != nil {
		return PluginInfo{}, err
	}

	pCfg := doc.Plugins[foundIdx]
	info := s.pluginInfoFromConfig(pCfg)
	s.enrichPluginInfoFromManifest(&info, pCfg)

	return info, nil
}

func readPluginMarketplaceManifest(root string) (pluginMarketplaceManifest, error) {
	path := filepath.Join(root, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return pluginMarketplaceManifest{}, fmt.Errorf("plugin service: read marketplace manifest: %w", err)
	}
	var manifest pluginMarketplaceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return pluginMarketplaceManifest{}, fmt.Errorf("plugin service: decode marketplace manifest: %w", err)
	}
	return manifest, nil
}

func findMarketplacePlugin(manifest pluginMarketplaceManifest, name string) (pluginMarketplaceEntry, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, entry := range manifest.Plugins {
		if strings.ToLower(strings.TrimSpace(entry.Name)) == name {
			return entry, true
		}
	}
	return pluginMarketplaceEntry{}, false
}

func safeJoinPluginPath(root string, rel string) (string, error) {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	joined, err := filepath.Abs(filepath.Join(rootAbs, strings.TrimSpace(rel)))
	if err != nil {
		return "", err
	}
	if !pluginregistry.PathWithinRoot(rootAbs, joined) {
		return "", fmt.Errorf("plugin service: plugin source path escapes marketplace root: %s", rel)
	}
	fi, err := os.Stat(joined)
	if err != nil {
		return "", fmt.Errorf("plugin service: plugin source path does not exist: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("plugin service: plugin source path is not a directory: %s", joined)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("plugin service: marketplace root is unavailable: %w", err)
	}
	joinedReal, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("plugin service: plugin source path does not exist: %w", err)
	}
	rootReal = filepath.Clean(rootReal)
	joinedReal = filepath.Clean(joinedReal)
	if !pluginregistry.PathWithinRoot(rootReal, joinedReal) {
		return "", fmt.Errorf("plugin service: plugin source path escapes marketplace root: %s", rel)
	}
	return joined, nil
}

func safePluginCacheName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		value = parsed.Host + parsed.Path
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "plugin"
	}
	return out
}

func findPluginConfigIndex(doc AppConfig, id string) (int, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	for i, pCfg := range doc.Plugins {
		if strings.ToLower(strings.TrimSpace(pCfg.ID)) == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("plugin service: plugin not found: %s", id)
}

func (s PluginService) pluginInfoFromConfig(pCfg PluginConfig) PluginInfo {
	info := PluginInfo{
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
		info.MCPServers = s.stack.MCPServersStatus(pCfg.ID)
	}
	return info
}

func (s PluginService) enrichPluginInfoFromManifest(info *PluginInfo, pCfg PluginConfig) {
	if info == nil {
		return
	}
	p, err := parseConfiguredPlugin(pCfg)
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

func pluginSkillDisplayNames(p pluginapi.InstalledPlugin) []string {
	bundles := pluginSkillBundles(p, true)
	if len(bundles) == 0 {
		return nil
	}
	metas, err := promptassembly.DiscoverPluginBundleMeta(bundles)
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

func pluginInfoHasMCPServer(info PluginInfo, name string) bool {
	for _, live := range info.MCPServers {
		if live.Name == name {
			return true
		}
	}
	return false
}

// cloneAppConfig returns a shallow clone of AppConfig with a fresh Plugins
// slice so mutations do not affect the original snapshot.
func cloneAppConfig(doc AppConfig) AppConfig {
	out := doc
	out.Plugins = clonePluginConfigs(doc.Plugins)
	out.PluginMarketplaces = append([]MarketplaceConfig(nil), doc.PluginMarketplaces...)
	return out
}

func (s PluginService) handleRebuildError(action string, err error, oldDoc AppConfig) error {
	rollbackErr := s.stack.store.Save(oldDoc)
	if rollbackErr != nil {
		return fmt.Errorf("plugin service: failed to %s (rollback config save failed): %w, rollback error: %w", action, err, rollbackErr)
	}
	if rbRebuildErr := s.stack.rebuildGateway(); rbRebuildErr != nil {
		return fmt.Errorf("plugin service: failed to %s (rollback rebuild failed): %w, rollback error: %w", action, err, rbRebuildErr)
	}
	return fmt.Errorf("plugin service: failed to %s (rollback successful): %w", action, err)
}

func (s PluginService) removeManagedPluginCache(cfg PluginConfig) error {
	if !cfg.Managed {
		return nil
	}
	cacheRoot := strings.TrimSpace(cfg.CacheRoot)
	if cacheRoot == "" {
		cacheRoot = managedPluginInstallCacheRoot(s.stack.storeDir, cfg.Root)
	}
	if cacheRoot == "" {
		return nil
	}
	installedRoot, err := filepath.Abs(filepath.Join(s.stack.storeDir, "plugins", "installed"))
	if err != nil {
		return err
	}
	cacheRoot, err = filepath.Abs(cacheRoot)
	if err != nil {
		return err
	}
	if !pluginregistry.PathWithinRoot(installedRoot, cacheRoot) || filepath.Clean(installedRoot) == filepath.Clean(cacheRoot) {
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
	if !pluginregistry.PathWithinRoot(installedRoot, pluginRoot) || filepath.Clean(installedRoot) == filepath.Clean(pluginRoot) {
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
	if !pluginregistry.PathWithinRoot(cacheRoot, pluginRoot) {
		return ""
	}
	return cacheRoot
}
