package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReclaimManagedCachesRemovesUnreferencedContent(t *testing.T) {
	storeDir := t.TempDir()
	cacheRoot := filepath.Join(storeDir, "plugins", "installed", "demo-cache")
	current := filepath.Join(cacheRoot, "revision-b")
	stale := filepath.Join(cacheRoot, "revision-a")
	for _, root := range []string{current, stale, filepath.Join(cacheRoot, ".staging")} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	host := &memoryHost{
		dir: storeDir,
		state: State{Plugins: []Config{{
			ID: "demo", Root: filepath.Join(current, "plugin"),
			Managed: true, CacheRoot: cacheRoot,
		}}},
	}
	if err := os.MkdirAll(filepath.Join(current, "plugin"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := NewService(host).ReclaimManagedCaches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("current managed content was reclaimed: %v", err)
	}
	for _, root := range []string{stale, filepath.Join(cacheRoot, ".staging")} {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("unreferenced managed content %q error = %v, want removed", root, err)
		}
	}
}

func TestReclaimManagedCachesWaitsForRuntimePin(t *testing.T) {
	storeDir := t.TempDir()
	cacheRoot := filepath.Join(storeDir, "plugins", "installed", "demo-cache")
	contentRoot := filepath.Join(cacheRoot, "revision-a")
	pluginRoot := filepath.Join(contentRoot, "plugin")
	if err := os.MkdirAll(pluginRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	host := &memoryHost{dir: storeDir}
	service := NewService(host)
	release := RetainManagedPluginCaches(storeDir, []Config{{
		ID: "demo", Root: pluginRoot, Enabled: true,
		Managed: true, CacheRoot: cacheRoot,
	}})

	if err := service.ReclaimManagedCaches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pluginRoot); err != nil {
		t.Fatalf("pinned managed content was reclaimed: %v", err)
	}
	release()
	if err := service.ReclaimManagedCaches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cacheRoot); !os.IsNotExist(err) {
		t.Fatalf("released managed cache error = %v, want removed", err)
	}
}
