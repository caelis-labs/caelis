package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPluginServiceRemoveOnlyDeletesManagedInstallCache(t *testing.T) {
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
	if _, err := stack.Plugins().addPath(ctx, managedPlugin, pluginAddPathOptions{Managed: true, CacheRoot: cacheRoot}); err != nil {
		t.Fatalf("add managed plugin: %v", err)
	}
	if err := stack.Plugins().Remove(ctx, "plugin"); err != nil {
		t.Fatalf("Remove(managed) error = %v", err)
	}
	if _, err := os.Stat(cacheRoot); !os.IsNotExist(err) {
		t.Fatalf("managed cache stat err = %v, want os.IsNotExist", err)
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
