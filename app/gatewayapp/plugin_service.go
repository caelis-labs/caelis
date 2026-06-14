package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/pluginregistry"
	"github.com/OnslaughtSnail/caelis/impl/tool/mcp"
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

type pluginMarketplaceManifest struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Plugins     []pluginMarketplaceEntry `json:"plugins"`
}

type pluginMarketplaceEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Version     string          `json:"version"`
	Source      json.RawMessage `json:"source"`
}

type pluginMarketplaceSource struct {
	Source string `json:"source"`
	URL    string `json:"url"`
	Repo   string `json:"repo"`
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
	Path   string `json:"path"`
}

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
	pluginRoot, err := s.resolveMarketplacePluginRoot(ctx, marketplaceRoot, entry)
	if err != nil {
		return PluginInfo{}, err
	}
	return s.AddPath(ctx, pluginRoot)
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

func (s PluginService) resolveMarketplaceRoot(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("plugin service: marketplace is required")
	}
	if absPath, err := filepath.Abs(ref); err == nil {
		if fi, statErr := os.Stat(absPath); statErr == nil && fi.IsDir() {
			return absPath, nil
		}
	}
	repoURL := marketplaceGitURL(ref)
	if repoURL == "" {
		return "", fmt.Errorf("plugin service: unsupported marketplace %q", ref)
	}
	root := filepath.Join(s.stack.storeDir, "plugins", "marketplaces", safePluginCacheName(ref))
	if err := cloneOrRefreshGitRepo(ctx, repoURL, "", root); err != nil {
		return "", fmt.Errorf("plugin service: fetch marketplace %q: %w", ref, err)
	}
	return root, nil
}

func (s PluginService) resolveMarketplacePluginRoot(ctx context.Context, marketplaceRoot string, entry pluginMarketplaceEntry) (string, error) {
	if len(entry.Source) == 0 {
		return "", fmt.Errorf("plugin service: marketplace plugin %q has no source", entry.Name)
	}
	var sourcePath string
	if err := json.Unmarshal(entry.Source, &sourcePath); err == nil && strings.TrimSpace(sourcePath) != "" {
		return safeJoinPluginPath(marketplaceRoot, sourcePath)
	}
	var source pluginMarketplaceSource
	if err := json.Unmarshal(entry.Source, &source); err != nil {
		return "", fmt.Errorf("plugin service: decode source for plugin %q: %w", entry.Name, err)
	}
	kind := strings.ToLower(strings.TrimSpace(source.Source))
	if kind == "" {
		kind = "url"
	}
	switch kind {
	case "github":
		if strings.TrimSpace(source.Repo) == "" {
			return "", fmt.Errorf("plugin service: github source for plugin %q is missing repo", entry.Name)
		}
		return s.cloneMarketplacePluginSource(ctx, "https://github.com/"+strings.TrimSpace(source.Repo)+".git", firstNonEmpty(source.Ref, source.SHA), source.Path, entry.Name)
	case "git", "git-subdir", "url":
		if strings.TrimSpace(source.URL) == "" {
			return "", fmt.Errorf("plugin service: %s source for plugin %q is missing url", kind, entry.Name)
		}
		return s.cloneMarketplacePluginSource(ctx, source.URL, firstNonEmpty(source.Ref, source.SHA), source.Path, entry.Name)
	default:
		return "", fmt.Errorf("plugin service: marketplace source type %q for plugin %q is not supported yet", kind, entry.Name)
	}
}

func (s PluginService) cloneMarketplacePluginSource(ctx context.Context, repoURL string, ref string, subpath string, pluginName string) (string, error) {
	root := filepath.Join(s.stack.storeDir, "plugins", "installed", safePluginCacheName(pluginName))
	if err := cloneOrRefreshGitRepo(ctx, repoURL, ref, root); err != nil {
		return "", err
	}
	if strings.TrimSpace(subpath) == "" {
		return root, nil
	}
	return safeJoinPluginPath(root, subpath)
}

// AddPath registers or updates a local plugin directory, enables it, and
// rebuilds the gateway. If the gateway rebuild fails the config is rolled back.
func (s PluginService) AddPath(ctx context.Context, path string) (PluginInfo, error) {
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

	p, err := pluginregistry.ParsePlugin(absPath)
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

	id := strings.ToLower(filepath.Base(absPath))
	var found bool
	for i, pCfg := range doc.Plugins {
		if strings.ToLower(pCfg.ID) == id {
			doc.Plugins[i].Root = absPath
			doc.Plugins[i].Name = p.Name
			doc.Plugins[i].Version = p.Version
			doc.Plugins[i].Description = p.Description
			doc.Plugins[i].Manifest = p.Manifest
			doc.Plugins[i].Kind = string(p.Kind)
			doc.Plugins[i].Enabled = true
			found = true
			break
		}
	}
	if !found {
		doc.Plugins = append(doc.Plugins, PluginConfig{
			ID:          id,
			Name:        p.Name,
			Root:        absPath,
			Manifest:    p.Manifest,
			Kind:        string(p.Kind),
			Enabled:     true,
			Version:     p.Version,
			Description: p.Description,
		})
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

	doc.Plugins = append(doc.Plugins[:foundIdx], doc.Plugins[foundIdx+1:]...)
	if err := s.stack.store.Save(doc); err != nil {
		return err
	}

	if err := s.stack.rebuildGateway(); err != nil {
		return s.handleRebuildError("rebuild gateway after removing plugin", err, oldDoc)
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

func marketplaceGitURL(ref string) string {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "":
		return ""
	case strings.EqualFold(ref, "claude-plugins-official"):
		return "https://github.com/anthropics/claude-plugins-official.git"
	case strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "ssh://") || strings.HasPrefix(ref, "git@"):
		return ref
	case strings.Count(ref, "/") == 1:
		return "https://github.com/" + ref + ".git"
	default:
		return ""
	}
}

func cloneOrRefreshGitRepo(ctx context.Context, repoURL string, ref string, root string) error {
	repoURL = strings.TrimSpace(repoURL)
	root = filepath.Clean(strings.TrimSpace(root))
	if repoURL == "" || root == "" || root == "." {
		return fmt.Errorf("invalid git source")
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return err
	}
	args := []string{"clone", "--depth", "1", repoURL, root}
	cmd := exec.CommandContext(ctx, "git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	if strings.TrimSpace(ref) != "" {
		if err := checkoutGitRef(ctx, root, strings.TrimSpace(ref)); err != nil {
			return err
		}
	}
	return nil
}

func checkoutGitRef(ctx context.Context, root string, ref string) error {
	fetch := exec.CommandContext(ctx, "git", "-C", root, "fetch", "--depth", "1", "origin", ref)
	if output, err := fetch.CombinedOutput(); err == nil {
		checkout := exec.CommandContext(ctx, "git", "-C", root, "checkout", "--detach", "FETCH_HEAD")
		if checkoutOutput, checkoutErr := checkout.CombinedOutput(); checkoutErr != nil {
			return fmt.Errorf("git checkout FETCH_HEAD: %w\n%s", checkoutErr, strings.TrimSpace(string(checkoutOutput)))
		}
		return nil
	} else {
		checkout := exec.CommandContext(ctx, "git", "-C", root, "checkout", ref)
		if checkoutOutput, checkoutErr := checkout.CombinedOutput(); checkoutErr != nil {
			return fmt.Errorf("git fetch %s: %w\n%s\ngit checkout %s: %w\n%s", ref, err, strings.TrimSpace(string(output)), ref, checkoutErr, strings.TrimSpace(string(checkoutOutput)))
		}
	}
	return nil
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
	if !pathWithinPluginRoot(rootAbs, joined) {
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
	if !pathWithinPluginRoot(rootReal, joinedReal) {
		return "", fmt.Errorf("plugin service: plugin source path escapes marketplace root: %s", rel)
	}
	return joined, nil
}

func pathWithinPluginRoot(root string, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel))
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
	p, err := pluginregistry.ParsePlugin(pCfg.Root)
	if err != nil {
		info.Status = "error"
		info.Warning = err.Error()
		return
	}
	info.Name = firstNonEmpty(info.Name, p.Name)
	info.Version = firstNonEmpty(info.Version, p.Version)
	info.Description = firstNonEmpty(info.Description, p.Description)
	for _, sc := range p.Skills {
		info.Skills = append(info.Skills, filepath.Base(sc.Root))
	}
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
