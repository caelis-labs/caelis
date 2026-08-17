package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPluginServiceRemoveKeepsManagedInstallCacheForActiveRuntimes(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	cacheRoot := filepath.Join(storeDir, "plugins", "installed", "cached-plugin")
	managedPlugin := filepath.Join(cacheRoot, "plugin")
	buildMinimalPluginDir(t, managedPlugin, `{"name":"managed","version":"1.0.0"}`)
	doc, err := stack.composition.authorities.store.Load()
	if err != nil {
		t.Fatalf("load plugin config: %v", err)
	}
	doc.Plugins = []PluginConfig{{
		ID:        "plugin",
		Name:      "managed",
		Root:      managedPlugin,
		Enabled:   true,
		Managed:   true,
		CacheRoot: cacheRoot,
	}}
	if err := stack.composition.authorities.store.Save(doc); err != nil {
		t.Fatalf("save managed plugin config: %v", err)
	}
	activated := activateFutureAssemblyRuntime(t, stack, "managed-plugin-runtime")
	if err := stack.Plugins().Remove(ctx, "plugin"); err != nil {
		t.Fatalf("Remove(managed) error = %v", err)
	}
	// Pure configuration removal must leave managed cache files in place so an
	// already-activated Session Runtime can keep reading them until release.
	if _, err := os.Stat(cacheRoot); err != nil {
		t.Fatalf("managed cache was reclaimed during pure config Remove: %v", err)
	}
	pinned, err := activated.pluginReads().Inspect(ctx, "plugin")
	if err != nil || !pinned.Enabled || pinned.Root != managedPlugin {
		t.Fatalf("active Runtime pinned Plugin = %#v, %v", pinned, err)
	}
	if err := stack.sessionRuntimes.release(ctx, "managed-plugin-runtime"); err != nil {
		t.Fatalf("release managed Plugin Runtime: %v", err)
	}
	if _, err := os.Stat(cacheRoot); !os.IsNotExist(err) {
		t.Fatalf("managed cache after Runtime release error = %v, want reclaimed", err)
	}

	localPlugin := filepath.Join(tmp, "localplugin")
	buildMinimalPluginDir(t, localPlugin, `{"name":"local","version":"1.0.0"}`)
	if _, err := stack.Plugins().AddPath(ctx, localPlugin); err != nil {
		t.Fatalf("AddPath(local) error = %v", err)
	}
	if err := stack.Plugins().Remove(ctx, "localplugin"); err != nil {
		t.Fatalf("Remove(local) error = %v", err)
	}
	if _, err := os.Stat(localPlugin); err != nil {
		t.Fatalf("local plugin was removed or unavailable: %v", err)
	}
}
