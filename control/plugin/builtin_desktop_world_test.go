package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestProductDesktopWorldPackageAndSessionPaths(t *testing.T) {
	store := t.TempDir()
	first, err := ProductContributions(RuntimePaths{ProductStoreDir: store, StoreDir: filepath.Join(store, "session-one")})
	if err != nil {
		t.Fatal(err)
	}
	if builtinDesktopWorldTarget == "" {
		if len(first.MCPServerSpecs) != 0 || len(first.Sources) != 1 || first.Sources[0].Status == "" {
			t.Fatal("unsupported target did not retain an unavailable capability")
		}
		return
	}
	second, err := ProductContributions(RuntimePaths{ProductStoreDir: store, StoreDir: filepath.Join(store, "session-two")})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.MCPServerSpecs) != 1 || len(first.SkillBundles) != 1 || len(first.Sources) != 1 {
		t.Fatal("fixed package was split or duplicated")
	}
	a, b := first.MCPServerSpecs[0], second.MCPServerSpecs[0]
	if a.PackageRoot != b.PackageRoot || a.DataDir == b.DataDir || a.PackageVersion != pinnedDesktopWorldTestVersion(t) || a.SourceTrusted || a.ToolResourceParameters["exec"] != "app" {
		t.Fatal("package sharing crossed Session data or approval boundaries")
	}
	if a.Command != filepath.Join(a.PackageRoot, "bin", filepath.Base(a.Command)) || len(a.Args) != 1 || a.Args[0] != "mcp" {
		t.Fatal("fixed package did not use its standard stdio executable")
	}
	if len(a.SourceTrustedTools) != 1 || a.SourceTrustedTools[0] != "apps" {
		t.Fatal("app execution acquired blanket builtin trust")
	}
}

func TestProductDesktopWorldConcurrentMaterializationAndTamper(t *testing.T) {
	if builtinDesktopWorldTarget == "" {
		t.Skip("no official package for this build target")
	}
	store := t.TempDir()
	var wg sync.WaitGroup
	roots := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { root, err := materializeDesktopWorld(context.Background(), store); roots <- root; errs <- err })
	}
	wg.Wait()
	close(roots)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	root := ""
	for candidate := range roots {
		if root != "" && root != candidate {
			t.Fatal("concurrent materialization published different sources")
		}
		root = candidate
	}
	skill := filepath.Join(root, "skills", "desktop-world", "SKILL.md")
	if err := os.WriteFile(skill, []byte("changed bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeDesktopWorld(context.Background(), store); err == nil {
		t.Fatal("tampered package was accepted/repaired")
	}
	data, _ := os.ReadFile(skill)
	if string(data) != "changed bytes" {
		t.Fatal("cache validation overwrote existing content")
	}
}

func TestProductDesktopWorldRejectsRedirectedCache(t *testing.T) {
	if builtinDesktopWorldTarget == "" {
		t.Skip("no official package for this build target")
	}
	store := t.TempDir()
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(store, "builtin-plugins")); err != nil {
		t.Skip("symlink creation unavailable")
	}
	if _, err := materializeDesktopWorld(context.Background(), store); err == nil {
		t.Fatal("builtin cache followed a foreign directory")
	}
	entries, _ := os.ReadDir(other)
	if len(entries) != 0 {
		t.Fatal("materialization wrote through redirected cache")
	}
}

func TestProductIdentityAliasRemainsVisibleWithoutRegistryMutation(t *testing.T) {
	root := t.TempDir()
	writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"0.1.3"}`)
	cfg := Config{ID: "user-alias", Root: root, Enabled: true}
	if !IsProductPlugin(cfg) {
		t.Fatal("manifest identity escaped fixed contribution ownership")
	}
	host := &memoryHost{dir: t.TempDir(), state: State{Plugins: []Config{cfg}}}
	info, err := NewService(host).Inspect(t.Context(), "user-alias")
	if err != nil || info.Status == "shadowed" || info.Version != "0.1.3" {
		t.Fatal("user override was hidden by the embedded fallback")
	}
	if len(host.mutations) != 0 {
		t.Fatal("source projection changed user installation")
	}
}

func pinnedDesktopWorldTestVersion(t *testing.T) string {
	t.Helper()
	var pin struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(builtinDesktopWorldPin, &pin); err != nil {
		t.Fatal(err)
	}
	return pin.Version
}
