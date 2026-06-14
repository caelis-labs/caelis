package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/configstore"
)

type MarketplaceInfo struct {
	Name                              string
	Description                       string
	Owner                             string
	Source                            string
	Root                              string
	Version                           string
	PluginRoot                        string
	AllowCrossMarketplaceDependencies []string
	PluginCount                       int
}

type pluginMarketplaceOwner struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type pluginMarketplaceManifest struct {
	Name                              string                    `json:"name"`
	Description                       string                    `json:"description"`
	Version                           string                    `json:"version"`
	Owner                             pluginMarketplaceOwner    `json:"owner"`
	Metadata                          pluginMarketplaceMetadata `json:"metadata"`
	AllowCrossMarketplaceDependencies []string                  `json:"allowCrossMarketplaceDependenciesOn"`
	Plugins                           []pluginMarketplaceEntry  `json:"plugins"`
}

type pluginMarketplaceMetadata struct {
	Description string `json:"description"`
	Version     string `json:"version"`
	PluginRoot  string `json:"pluginRoot"`
}

type pluginMarketplaceEntry struct {
	Name         string          `json:"name"`
	DisplayName  string          `json:"displayName"`
	Description  string          `json:"description"`
	Version      string          `json:"version"`
	Category     string          `json:"category"`
	Tags         []string        `json:"tags"`
	Strict       *bool           `json:"strict"`
	Author       json.RawMessage `json:"author"`
	Dependencies json.RawMessage `json:"dependencies"`
	LSPServers   json.RawMessage `json:"lspServers"`
	Source       json.RawMessage `json:"source"`
}

type pluginMarketplaceSource struct {
	Source   string `json:"source"`
	URL      string `json:"url"`
	Repo     string `json:"repo"`
	Ref      string `json:"ref"`
	SHA      string `json:"sha"`
	Path     string `json:"path"`
	Package  string `json:"package"`
	Version  string `json:"version"`
	Registry string `json:"registry"`
}

// AddMarketplace registers a Claude Code compatible marketplace and persists it.
func (s PluginService) AddMarketplace(ctx context.Context, source string) (MarketplaceInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return MarketplaceInfo{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return MarketplaceInfo{}, fmt.Errorf("plugin service: marketplace source is required")
	}
	root, repoURL, err := s.fetchMarketplaceRoot(ctx, source)
	if err != nil {
		return MarketplaceInfo{}, err
	}
	manifest, err := readPluginMarketplaceManifest(root)
	if err != nil {
		return MarketplaceInfo{}, err
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return MarketplaceInfo{}, fmt.Errorf("plugin service: marketplace manifest is missing name")
	}

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("add marketplace"); err != nil {
		return MarketplaceInfo{}, err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return MarketplaceInfo{}, err
	}
	doc := cloneAppConfig(oldDoc)
	entry := MarketplaceConfig{
		Name:                              strings.TrimSpace(manifest.Name),
		Description:                       marketplaceDescription(manifest),
		Owner:                             strings.TrimSpace(manifest.Owner.Name),
		Source:                            source,
		Root:                              root,
		Version:                           marketplaceVersion(manifest),
		RepoURL:                           repoURL,
		PluginRoot:                        strings.TrimSpace(manifest.Metadata.PluginRoot),
		AllowCrossMarketplaceDependencies: normalizeMarketplaceNameList(manifest.AllowCrossMarketplaceDependencies),
	}
	doc.PluginMarketplaces = configstore.UpsertMarketplaceConfig(doc.PluginMarketplaces, entry)
	if err := s.stack.store.Save(doc); err != nil {
		return MarketplaceInfo{}, err
	}
	return marketplaceInfoFromManifest(entry, manifest), nil
}

func (s PluginService) ListMarketplaces(ctx context.Context) ([]MarketplaceInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return nil, fmt.Errorf("plugin service: stack store is unavailable")
	}
	doc, err := s.stack.store.Load()
	if err != nil {
		return nil, err
	}
	out := make([]MarketplaceInfo, 0, len(doc.PluginMarketplaces))
	for _, entry := range doc.PluginMarketplaces {
		info := marketplaceInfoFromConfig(entry)
		if manifest, err := readPluginMarketplaceManifest(entry.Root); err == nil {
			info.PluginCount = len(manifest.Plugins)
			if info.Description == "" {
				info.Description = strings.TrimSpace(manifest.Description)
			}
			if info.Version == "" {
				info.Version = strings.TrimSpace(manifest.Version)
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func (s PluginService) UpdateMarketplace(ctx context.Context, name string) (MarketplaceInfo, error) {
	if s.stack == nil || s.stack.store == nil {
		return MarketplaceInfo{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	name = strings.ToLower(strings.TrimSpace(name))
	doc, err := s.stack.store.Load()
	if err != nil {
		return MarketplaceInfo{}, err
	}
	_, entry, ok := findMarketplaceConfig(doc, name)
	if !ok {
		return MarketplaceInfo{}, fmt.Errorf("plugin service: marketplace not found: %s", name)
	}
	root, repoURL, err := s.fetchMarketplaceRoot(ctx, entry.Source)
	if err != nil {
		return MarketplaceInfo{}, err
	}
	manifest, err := readPluginMarketplaceManifest(root)
	if err != nil {
		return MarketplaceInfo{}, err
	}

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("update marketplace"); err != nil {
		return MarketplaceInfo{}, err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return MarketplaceInfo{}, err
	}
	doc = cloneAppConfig(oldDoc)
	_, entry, ok = findMarketplaceConfig(doc, name)
	if !ok {
		return MarketplaceInfo{}, fmt.Errorf("plugin service: marketplace not found: %s", name)
	}
	entry.Root = root
	entry.RepoURL = repoURL
	entry.Description = marketplaceDescription(manifest)
	entry.Version = marketplaceVersion(manifest)
	entry.Owner = strings.TrimSpace(manifest.Owner.Name)
	entry.PluginRoot = strings.TrimSpace(manifest.Metadata.PluginRoot)
	entry.AllowCrossMarketplaceDependencies = normalizeMarketplaceNameList(manifest.AllowCrossMarketplaceDependencies)
	doc.PluginMarketplaces = configstore.UpsertMarketplaceConfig(doc.PluginMarketplaces, entry)
	if err := s.stack.store.Save(doc); err != nil {
		return MarketplaceInfo{}, err
	}
	return marketplaceInfoFromManifest(entry, manifest), nil
}

func (s PluginService) RemoveMarketplace(ctx context.Context, name string) error {
	if s.stack == nil || s.stack.store == nil {
		return fmt.Errorf("plugin service: stack store is unavailable")
	}
	name = strings.ToLower(strings.TrimSpace(name))

	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("remove marketplace"); err != nil {
		return err
	}

	oldDoc, err := s.stack.store.Load()
	if err != nil {
		return err
	}
	doc := cloneAppConfig(oldDoc)
	idx, _, ok := findMarketplaceConfig(doc, name)
	if !ok {
		return fmt.Errorf("plugin service: marketplace not found: %s", name)
	}
	doc.PluginMarketplaces = append(doc.PluginMarketplaces[:idx], doc.PluginMarketplaces[idx+1:]...)
	return s.stack.store.Save(doc)
}

func (s PluginService) fetchMarketplaceRoot(ctx context.Context, source string) (root string, repoURL string, err error) {
	source = strings.TrimSpace(source)
	if absPath, absErr := filepath.Abs(source); absErr == nil {
		if fi, statErr := os.Stat(absPath); statErr == nil && fi.IsDir() {
			if _, readErr := readPluginMarketplaceManifest(absPath); readErr != nil {
				return "", "", readErr
			}
			return absPath, "", nil
		}
	}
	repoURL, err = resolveMarketplaceGitURL(source)
	if err != nil {
		return "", "", err
	}
	root = filepath.Join(s.stack.storeDir, "plugins", "marketplaces", marketplaceCacheDirName(source))
	if err := cloneOrRefreshGitRepo(ctx, repoURL, "", root, ""); err != nil {
		return "", "", fmt.Errorf("plugin service: fetch marketplace %q: %w", source, err)
	}
	if _, err := readPluginMarketplaceManifest(root); err != nil {
		return "", "", err
	}
	return root, repoURL, nil
}

func (s PluginService) resolveMarketplaceRoot(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("plugin service: marketplace is required")
	}
	if s.stack != nil && s.stack.store != nil {
		doc, err := s.stack.store.Load()
		if err == nil {
			if _, entry, ok := findMarketplaceConfig(doc, ref); ok {
				if strings.TrimSpace(entry.Root) != "" {
					if fi, statErr := os.Stat(entry.Root); statErr == nil && fi.IsDir() {
						return entry.Root, nil
					}
				}
				if strings.TrimSpace(entry.Source) != "" {
					root, _, err := s.fetchMarketplaceRoot(ctx, entry.Source)
					if err != nil {
						return "", err
					}
					return root, nil
				}
			}
		}
	}
	if absPath, err := filepath.Abs(ref); err == nil {
		if fi, statErr := os.Stat(absPath); statErr == nil && fi.IsDir() {
			return absPath, nil
		}
	}
	root, _, err := s.fetchMarketplaceRoot(ctx, ref)
	return root, err
}

func (s PluginService) resolveMarketplacePluginRoot(ctx context.Context, marketplaceRoot string, manifest pluginMarketplaceManifest, entry pluginMarketplaceEntry) (string, error) {
	if len(entry.Source) == 0 {
		return "", fmt.Errorf("plugin service: marketplace plugin %q has no source", entry.Name)
	}
	if err := validateMarketplaceEntryRuntimeSupport(manifest, entry); err != nil {
		return "", err
	}
	var sourcePath string
	if err := json.Unmarshal(entry.Source, &sourcePath); err == nil && strings.TrimSpace(sourcePath) != "" {
		rel, err := marketplaceRelativePluginPath(manifest, sourcePath, entry.Name)
		if err != nil {
			return "", err
		}
		return safeJoinPluginPath(marketplaceRoot, rel)
	}
	var source pluginMarketplaceSource
	if err := json.Unmarshal(entry.Source, &source); err != nil {
		return "", fmt.Errorf("plugin service: decode source for plugin %q: %w", entry.Name, err)
	}
	kind := strings.ToLower(strings.TrimSpace(source.Source))
	if kind == "" {
		kind = "url"
	}
	cacheKey := pluginInstallCacheKey{
		Marketplace: strings.TrimSpace(manifest.Name),
		PluginName:  strings.TrimSpace(entry.Name),
		Subpath:     strings.TrimSpace(source.Path),
		Ref:         firstNonEmpty(source.Ref, source.SHA),
	}
	switch kind {
	case "github":
		repo, err := validateGitHubRepo(source.Repo)
		if err != nil {
			return "", fmt.Errorf("plugin service: github source for plugin %q: %w", entry.Name, err)
		}
		repoURL, err := resolveGitHubCloneURL(repo)
		if err != nil {
			return "", err
		}
		cacheKey.RepoURL = repoURL
		return s.cloneMarketplacePluginSource(ctx, repoURL, source.Ref, source.SHA, source.Path, cacheKey)
	case "url", "git":
		if strings.TrimSpace(source.URL) == "" {
			return "", fmt.Errorf("plugin service: %s source for plugin %q is missing url", kind, entry.Name)
		}
		repoURL, err := resolvePluginSourceGitURL(source.URL)
		if err != nil {
			return "", err
		}
		cacheKey.RepoURL = repoURL
		return s.cloneMarketplacePluginSource(ctx, repoURL, source.Ref, source.SHA, source.Path, cacheKey)
	case "git-subdir":
		if strings.TrimSpace(source.URL) == "" {
			return "", fmt.Errorf("plugin service: git-subdir source for plugin %q is missing url", entry.Name)
		}
		if strings.TrimSpace(source.Path) == "" {
			return "", fmt.Errorf("plugin service: git-subdir source for plugin %q is missing path", entry.Name)
		}
		repoURL, err := resolvePluginSourceGitURL(source.URL)
		if err != nil {
			return "", err
		}
		cacheKey.RepoURL = repoURL
		return s.cloneMarketplacePluginSource(ctx, repoURL, source.Ref, source.SHA, source.Path, cacheKey)
	case "npm":
		return s.resolveNPMSource(ctx, source, cacheKey)
	default:
		return "", fmt.Errorf("plugin service: marketplace source type %q for plugin %q is not supported", kind, entry.Name)
	}
}

type pluginMarketplaceDependency struct {
	Name        string
	Marketplace string
	Version     string
}

func (s PluginService) cloneMarketplacePluginSource(ctx context.Context, repoURL string, ref string, sha string, subpath string, cacheKey pluginInstallCacheKey) (string, error) {
	cacheKey.RepoURL = strings.TrimSpace(repoURL)
	cacheKey.Ref = strings.TrimSpace(firstNonEmpty(ref, sha))
	cacheKey.Subpath = strings.TrimSpace(subpath)
	root := filepath.Join(s.stack.storeDir, "plugins", "installed", pluginInstallCacheDirName(cacheKey))
	if err := cloneOrRefreshGitRepo(ctx, repoURL, ref, root, sha); err != nil {
		return "", err
	}
	if strings.TrimSpace(subpath) == "" {
		return root, nil
	}
	return safeJoinPluginPath(root, subpath)
}

func (s PluginService) resolveNPMSource(_ context.Context, source pluginMarketplaceSource, _ pluginInstallCacheKey) (string, error) {
	pkg := strings.TrimSpace(source.Package)
	if pkg == "" {
		return "", fmt.Errorf("plugin service: npm source is missing package")
	}
	return "", fmt.Errorf("plugin service: npm plugin %q cannot be loaded: caelis does not execute OpenCode/Claude npm plugin runtimes", pkg)
}

func validateMarketplaceEntryRuntimeSupport(manifest pluginMarketplaceManifest, entry pluginMarketplaceEntry) error {
	if jsonRawHasValue(entry.LSPServers) {
		return fmt.Errorf("plugin service: marketplace plugin %q declares lspServers, which caelis does not consume yet", entry.Name)
	}
	if !jsonRawHasValue(entry.Dependencies) {
		return nil
	}
	deps, err := decodeMarketplaceDependencies(entry.Dependencies)
	if err != nil {
		return fmt.Errorf("plugin service: decode dependencies for plugin %q: %w", entry.Name, err)
	}
	if len(deps) == 0 {
		return nil
	}
	for _, dep := range deps {
		targetMarketplace := firstNonEmpty(dep.Marketplace, manifest.Name)
		if !strings.EqualFold(strings.TrimSpace(targetMarketplace), strings.TrimSpace(manifest.Name)) &&
			!marketplaceNameAllowed(manifest.AllowCrossMarketplaceDependencies, targetMarketplace) {
			return fmt.Errorf("plugin service: cross-marketplace dependency %q@%s for plugin %q is blocked; add %q to allowCrossMarketplaceDependenciesOn", dep.Name, targetMarketplace, entry.Name, targetMarketplace)
		}
	}
	return fmt.Errorf("plugin service: marketplace plugin %q declares dependencies, but caelis does not resolve plugin dependencies yet", entry.Name)
}

func marketplaceRelativePluginPath(manifest pluginMarketplaceManifest, sourcePath string, pluginName string) (string, error) {
	trimmed := strings.TrimSpace(sourcePath)
	if strings.HasPrefix(trimmed, "./") {
		return strings.TrimPrefix(trimmed, "./"), nil
	}
	pluginRoot := strings.TrimSpace(manifest.Metadata.PluginRoot)
	if pluginRoot == "" {
		return "", fmt.Errorf("plugin service: relative plugin source for %q must start with ./", pluginName)
	}
	if !strings.HasPrefix(pluginRoot, "./") {
		return "", fmt.Errorf("plugin service: metadata.pluginRoot for marketplace %q must start with ./", manifest.Name)
	}
	if filepath.IsAbs(trimmed) || strings.HasPrefix(trimmed, "../") || trimmed == ".." || strings.Contains(trimmed, string(filepath.Separator)+".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin service: plugin source path escapes marketplace root: %s", sourcePath)
	}
	return filepath.Join(strings.TrimPrefix(pluginRoot, "./"), trimmed), nil
}

func decodeMarketplaceDependencies(raw json.RawMessage) ([]pluginMarketplaceDependency, error) {
	if !jsonRawHasValue(raw) {
		return nil, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	out := make([]pluginMarketplaceDependency, 0, len(values))
	for _, value := range values {
		var name string
		if err := json.Unmarshal(value, &name); err == nil && strings.TrimSpace(name) != "" {
			out = append(out, pluginMarketplaceDependency{Name: strings.TrimSpace(name)})
			continue
		}
		var obj struct {
			Name        string `json:"name"`
			Marketplace string `json:"marketplace"`
			Version     string `json:"version"`
		}
		if err := json.Unmarshal(value, &obj); err != nil {
			return nil, err
		}
		if strings.TrimSpace(obj.Name) == "" {
			return nil, fmt.Errorf("dependency is missing name")
		}
		out = append(out, pluginMarketplaceDependency{
			Name:        strings.TrimSpace(obj.Name),
			Marketplace: strings.TrimSpace(obj.Marketplace),
			Version:     strings.TrimSpace(obj.Version),
		})
	}
	return out, nil
}

func jsonRawHasValue(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null" && trimmed != "[]"
}

func marketplaceNameAllowed(values []string, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == name {
			return true
		}
	}
	return false
}

func marketplaceDescription(manifest pluginMarketplaceManifest) string {
	return firstNonEmpty(manifest.Description, manifest.Metadata.Description)
}

func marketplaceVersion(manifest pluginMarketplaceManifest) string {
	return firstNonEmpty(manifest.Version, manifest.Metadata.Version)
}

func normalizeMarketplaceNameList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func marketplaceInfoFromConfig(entry MarketplaceConfig) MarketplaceInfo {
	return MarketplaceInfo{
		Name:                              entry.Name,
		Description:                       entry.Description,
		Owner:                             entry.Owner,
		Source:                            entry.Source,
		Root:                              entry.Root,
		Version:                           entry.Version,
		PluginRoot:                        entry.PluginRoot,
		AllowCrossMarketplaceDependencies: append([]string(nil), entry.AllowCrossMarketplaceDependencies...),
	}
}

func marketplaceInfoFromManifest(entry MarketplaceConfig, manifest pluginMarketplaceManifest) MarketplaceInfo {
	return MarketplaceInfo{
		Name:                              firstNonEmpty(entry.Name, manifest.Name),
		Description:                       firstNonEmpty(entry.Description, marketplaceDescription(manifest)),
		Owner:                             firstNonEmpty(entry.Owner, manifest.Owner.Name),
		Source:                            entry.Source,
		Root:                              entry.Root,
		Version:                           firstNonEmpty(entry.Version, marketplaceVersion(manifest)),
		PluginRoot:                        firstNonEmpty(entry.PluginRoot, manifest.Metadata.PluginRoot),
		AllowCrossMarketplaceDependencies: normalizeMarketplaceNameList(append(entry.AllowCrossMarketplaceDependencies, manifest.AllowCrossMarketplaceDependencies...)),
		PluginCount:                       len(manifest.Plugins),
	}
}

func findMarketplaceConfig(doc AppConfig, name string) (int, MarketplaceConfig, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for i, entry := range doc.PluginMarketplaces {
		if strings.ToLower(strings.TrimSpace(entry.Name)) == name {
			return i, entry, true
		}
	}
	return -1, MarketplaceConfig{}, false
}
