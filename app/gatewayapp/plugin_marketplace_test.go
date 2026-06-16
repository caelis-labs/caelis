package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginInstallCacheDirNameSeparatesSameNameDifferentRepo(t *testing.T) {
	t.Parallel()

	a := pluginInstallCacheDirName(pluginInstallCacheKey{
		RepoURL:    "https://github.com/acme/one.git",
		Ref:        "main",
		PluginName: "shared-name",
	})
	b := pluginInstallCacheDirName(pluginInstallCacheKey{
		RepoURL:    "https://github.com/acme/two.git",
		Ref:        "main",
		PluginName: "shared-name",
	})
	if a == b {
		t.Fatalf("cache dirs = %q, want different repos to produce different cache keys", a)
	}
}

func TestPluginInstallCacheDirNameSeparatesDifferentRef(t *testing.T) {
	t.Parallel()

	a := pluginInstallCacheDirName(pluginInstallCacheKey{
		RepoURL:    "https://github.com/acme/repo.git",
		Ref:        "v1",
		PluginName: "plugin-a",
	})
	b := pluginInstallCacheDirName(pluginInstallCacheKey{
		RepoURL:    "https://github.com/acme/repo.git",
		Ref:        "v2",
		PluginName: "plugin-a",
	})
	if a == b {
		t.Fatalf("cache dirs = %q, want different refs to produce different cache keys", a)
	}
}

func TestMarketplaceCacheDirNameSeparatesSimilarRefs(t *testing.T) {
	t.Parallel()

	a := marketplaceCacheDirName("anthropics/claude-plugins-official")
	b := marketplaceCacheDirName("anthropics/claude-plugins-official@v1")
	if a == b {
		t.Fatalf("cache dirs = %q, want distinct marketplace cache names", a)
	}
}

func TestResolveNPMSourceDoesNotCreateUnsupportedCache(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	stack := buildPluginStack(t, storeDir, filepath.Join(tmp, "ws"))
	key := pluginInstallCacheKey{
		Marketplace: "demo-market",
		PluginName:  "npm-plugin",
	}
	source := pluginMarketplaceSource{
		Package:  "@acme/npm-plugin",
		Version:  "1.0.0",
		Registry: "https://registry.npmjs.org",
	}
	legacyKey := key
	legacyKey.RepoURL = "npm:" + source.Package + "@" + source.Version
	legacyKey.Ref = source.Version
	legacyKey.Subpath = source.Registry
	legacyRoot := filepath.Join(storeDir, "plugins", "installed", pluginInstallCacheDirName(legacyKey))

	if _, err := stack.Plugins().resolveNPMSource(context.Background(), source, key); err == nil {
		t.Fatalf("resolveNPMSource() error = nil, want unsupported npm error")
	}
	if _, err := os.Stat(legacyRoot); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) = %v, want no unsupported cache artifact", legacyRoot, err)
	}
}

func TestValidateGitCloneURLRejectsMaliciousSources(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"file:///etc/passwd",
		"/tmp/evil.git",
		"ftp://example.com/repo.git",
		"javascript:alert(1)",
	} {
		if _, err := validateGitCloneURL(raw); err == nil {
			t.Fatalf("validateGitCloneURL(%q) error = nil, want rejection", raw)
		}
	}
}

func TestValidateGitCloneURLAcceptsHTTPSAndGitAt(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"https://github.com/acme/repo.git",
		"git@github.com:acme/repo.git",
	} {
		if got, err := validateGitCloneURL(raw); err != nil || got != raw {
			t.Fatalf("validateGitCloneURL(%q) = (%q, %v), want accepted url", raw, got, err)
		}
	}
}

func TestResolveMarketplaceGitURLAcceptsGitHubShorthand(t *testing.T) {
	t.Parallel()

	got, err := resolveMarketplaceGitURL("acme/plugins")
	if err != nil {
		t.Fatalf("resolveMarketplaceGitURL() error = %v", err)
	}
	if got != "https://github.com/acme/plugins.git" {
		t.Fatalf("resolveMarketplaceGitURL() = %q, want github shorthand expansion", got)
	}
}

func TestValidateGitHubRepoRejectsArbitraryStrings(t *testing.T) {
	t.Parallel()

	for _, repo := range []string{"", "not-a-repo", "owner/", "/repo", "https://evil.com/x/y"} {
		if _, err := validateGitHubRepo(repo); err == nil {
			t.Fatalf("validateGitHubRepo(%q) error = nil, want rejection", repo)
		}
	}
}

func TestResolveMarketplacePluginRootRejectsRelativePathWithoutDotSlash(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	marketplaceDir := filepath.Join(tmp, "marketplace")
	pluginDir := filepath.Join(marketplaceDir, "plugins", "demo")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	stack := buildPluginStack(t, filepath.Join(tmp, "store"), filepath.Join(tmp, "ws"))
	_, err := stack.Plugins().resolveMarketplacePluginRoot(context.Background(), marketplaceDir, pluginMarketplaceManifest{Name: "demo-market"}, pluginMarketplaceEntry{
		Name:   "demo",
		Source: []byte(`"plugins/demo"`),
	})
	if err == nil || !strings.Contains(err.Error(), "./") {
		t.Fatalf("resolveMarketplacePluginRoot() error = %v, want ./ prefix requirement", err)
	}
}

func TestResolveMarketplacePluginRootUsesMetadataPluginRoot(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	marketplaceDir := filepath.Join(tmp, "marketplace")
	pluginDir := filepath.Join(marketplaceDir, "plugins", "demo")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	stack := buildPluginStack(t, filepath.Join(tmp, "store"), filepath.Join(tmp, "ws"))
	root, err := stack.Plugins().resolveMarketplacePluginRoot(context.Background(), marketplaceDir, pluginMarketplaceManifest{
		Name:     "demo-market",
		Metadata: pluginMarketplaceMetadata{PluginRoot: "./plugins"},
	}, pluginMarketplaceEntry{
		Name:   "demo",
		Source: []byte(`"demo"`),
	})
	if err != nil {
		t.Fatalf("resolveMarketplacePluginRoot() error = %v", err)
	}
	if root != pluginDir {
		t.Fatalf("resolveMarketplacePluginRoot() = %q, want %q", root, pluginDir)
	}
}

func TestResolveMarketplacePluginRootRejectsUnsupportedLSPServers(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	marketplaceDir := filepath.Join(tmp, "marketplace")
	pluginDir := filepath.Join(marketplaceDir, "plugins", "demo")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	stack := buildPluginStack(t, filepath.Join(tmp, "store"), filepath.Join(tmp, "ws"))
	_, err := stack.Plugins().resolveMarketplacePluginRoot(context.Background(), marketplaceDir, pluginMarketplaceManifest{Name: "demo-market"}, pluginMarketplaceEntry{
		Name:       "demo",
		Source:     []byte(`"./plugins/demo"`),
		LSPServers: []byte(`{"go":{}}`),
	})
	if err == nil || !strings.Contains(err.Error(), "lspServers") {
		t.Fatalf("resolveMarketplacePluginRoot() error = %v, want lspServers rejection", err)
	}
}

func TestResolveMarketplacePluginRootRejectsBlockedCrossMarketplaceDependency(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	marketplaceDir := filepath.Join(tmp, "marketplace")
	pluginDir := filepath.Join(marketplaceDir, "plugins", "demo")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	stack := buildPluginStack(t, filepath.Join(tmp, "store"), filepath.Join(tmp, "ws"))
	_, err := stack.Plugins().resolveMarketplacePluginRoot(context.Background(), marketplaceDir, pluginMarketplaceManifest{Name: "demo-market"}, pluginMarketplaceEntry{
		Name:         "demo",
		Source:       []byte(`"./plugins/demo"`),
		Dependencies: []byte(`[{"name":"shared","marketplace":"shared-market"}]`),
	})
	if err == nil || !strings.Contains(err.Error(), "allowCrossMarketplaceDependenciesOn") {
		t.Fatalf("resolveMarketplacePluginRoot() error = %v, want cross-marketplace allowlist rejection", err)
	}
}

func TestResolvePluginSourceGitURLAcceptsGitHubShorthand(t *testing.T) {
	t.Parallel()

	got, err := resolvePluginSourceGitURL("acme/plugin")
	if err != nil {
		t.Fatalf("resolvePluginSourceGitURL() error = %v", err)
	}
	if got != "https://github.com/acme/plugin.git" {
		t.Fatalf("resolvePluginSourceGitURL() = %q, want github clone url", got)
	}
}

func TestMarketplaceAddListInstallUpdateRoundTrip(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	marketplaceDir := filepath.Join(tmp, "marketplace")
	pluginDir := filepath.Join(marketplaceDir, "plugins", "demo-plugin")
	manifestDir := filepath.Join(pluginDir, ".claude-plugin")
	marketplaceManifestDir := filepath.Join(marketplaceDir, ".claude-plugin")
	for _, dir := range []string{storeDir, workspaceDir, manifestDir, marketplaceManifestDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte(`{"name":"Demo Plugin","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceManifestDir, "marketplace.json"), []byte(`{
		"name": "demo-market",
		"description": "Demo marketplace",
		"owner": {"name": "Demo Owner"},
		"plugins": [
			{"name": "demo-plugin", "source": "./plugins/demo-plugin", "description": "Demo"}
		]
	}`), 0o600); err != nil {
		t.Fatalf("write marketplace manifest: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	added, err := stack.Plugins().AddMarketplace(ctx, marketplaceDir)
	if err != nil {
		t.Fatalf("AddMarketplace() error = %v", err)
	}
	if added.Name != "demo-market" || added.PluginCount != 1 {
		t.Fatalf("AddMarketplace() = %#v, want demo-market with one plugin", added)
	}

	listed, err := stack.Plugins().ListMarketplaces(ctx)
	if err != nil || len(listed) != 1 || listed[0].Name != "demo-market" {
		t.Fatalf("ListMarketplaces() = %#v, %v, want persisted marketplace", listed, err)
	}

	info, err := stack.Plugins().Install(ctx, "demo-plugin@demo-market")
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if info.ID != "demo-plugin" || !info.Enabled {
		t.Fatalf("Install() = %#v, want enabled demo-plugin", info)
	}

	updated, err := stack.Plugins().UpdateMarketplace(ctx, "demo-market")
	if err != nil {
		t.Fatalf("UpdateMarketplace() error = %v", err)
	}
	if updated.Name != "demo-market" {
		t.Fatalf("UpdateMarketplace() = %#v", updated)
	}
}

func TestAddMarketplaceAllowsMissingOwner(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	marketplaceDir := filepath.Join(tmp, "marketplace")
	marketplaceManifestDir := filepath.Join(marketplaceDir, ".claude-plugin")
	if err := os.MkdirAll(marketplaceManifestDir, 0o700); err != nil {
		t.Fatalf("mkdir marketplace manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceManifestDir, "marketplace.json"), []byte(`{
		"name": "ownerless-market",
		"plugins": []
	}`), 0o600); err != nil {
		t.Fatalf("write marketplace manifest: %v", err)
	}

	stack := buildPluginStack(t, filepath.Join(tmp, "store"), filepath.Join(tmp, "ws"))
	added, err := stack.Plugins().AddMarketplace(context.Background(), marketplaceDir)
	if err != nil {
		t.Fatalf("AddMarketplace() error = %v", err)
	}
	if added.Name != "ownerless-market" || added.Owner != "" {
		t.Fatalf("AddMarketplace() = %#v, want ownerless-market with empty owner", added)
	}
}

func TestInstallFromRegisteredMarketplaceRefetchesMissingRoot(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	marketplaceDir := filepath.Join(tmp, "marketplace")
	pluginDir := filepath.Join(marketplaceDir, "plugins", "demo-plugin")
	manifestDir := filepath.Join(pluginDir, ".claude-plugin")
	marketplaceManifestDir := filepath.Join(marketplaceDir, ".claude-plugin")
	for _, dir := range []string{storeDir, workspaceDir, manifestDir, marketplaceManifestDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte(`{"name":"Demo Plugin","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceManifestDir, "marketplace.json"), []byte(`{
		"name": "demo-market",
		"owner": {"name": "Demo Owner"},
		"plugins": [
			{"name": "demo-plugin", "source": "./plugins/demo-plugin"}
		]
	}`), 0o600); err != nil {
		t.Fatalf("write marketplace manifest: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()
	if _, err := stack.Plugins().AddMarketplace(ctx, marketplaceDir); err != nil {
		t.Fatalf("AddMarketplace() error = %v", err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	doc.PluginMarketplaces[0].Root = filepath.Join(tmp, "missing-cache-root")
	if err := stack.store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	info, err := stack.Plugins().Install(ctx, "demo-plugin@demo-market")
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if info.ID != "demo-plugin" || info.Root != pluginDir {
		t.Fatalf("Install() = %#v, want demo-plugin from saved marketplace source", info)
	}
}

func TestSafeJoinPluginPathRejectsTraversalWithoutDotSlash(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	marketplaceDir := filepath.Join(tmp, "marketplace")
	if err := os.MkdirAll(marketplaceDir, 0o700); err != nil {
		t.Fatalf("mkdir marketplace: %v", err)
	}
	for _, rel := range []string{"../outside", "../../outside"} {
		if _, err := safeJoinPluginPath(marketplaceDir, rel); err == nil {
			t.Fatalf("safeJoinPluginPath(%q) error = nil, want rejection", rel)
		}
	}
}
