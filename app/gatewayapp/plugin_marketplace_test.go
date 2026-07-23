package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMarketplaceAddListInstallUpdateRoundTrip(t *testing.T) {
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
