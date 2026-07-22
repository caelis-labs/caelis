package plugin

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
	service := NewService(&memoryHost{dir: storeDir})
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

	if _, err := service.resolveNPMSource(context.Background(), source, key); err == nil {
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
	service := NewService(&memoryHost{dir: filepath.Join(tmp, "store")})
	_, err := service.resolveMarketplacePluginRoot(context.Background(), marketplaceDir, pluginMarketplaceManifest{Name: "demo-market"}, pluginMarketplaceEntry{
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
	service := NewService(&memoryHost{dir: filepath.Join(tmp, "store")})
	root, err := service.resolveMarketplacePluginRoot(context.Background(), marketplaceDir, pluginMarketplaceManifest{
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
	service := NewService(&memoryHost{dir: filepath.Join(tmp, "store")})
	_, err := service.resolveMarketplacePluginRoot(context.Background(), marketplaceDir, pluginMarketplaceManifest{Name: "demo-market"}, pluginMarketplaceEntry{
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
	service := NewService(&memoryHost{dir: filepath.Join(tmp, "store")})
	_, err := service.resolveMarketplacePluginRoot(context.Background(), marketplaceDir, pluginMarketplaceManifest{Name: "demo-market"}, pluginMarketplaceEntry{
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

func TestSafeJoinPluginPathRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	marketplaceDir := filepath.Join(tmp, "marketplace")
	pluginsDir := filepath.Join(marketplaceDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o700); err != nil {
		t.Fatalf("mkdir plugins dir: %v", err)
	}
	outsideDir := filepath.Join(tmp, "outside-plugin")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("mkdir outside plugin dir: %v", err)
	}
	linkPath := filepath.Join(pluginsDir, "escape")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("symlink unavailable on this platform: %v", err)
	}

	_, err := safeJoinPluginPath(marketplaceDir, "plugins/escape")
	if err == nil {
		t.Fatal("safeJoinPluginPath() error = nil, want symlink escape rejection")
	}
	if !strings.Contains(err.Error(), "escapes marketplace root") {
		t.Fatalf("safeJoinPluginPath() error = %v, want escape rejection", err)
	}
}
