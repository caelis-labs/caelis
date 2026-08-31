package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func writeServiceTestPlugin(t *testing.T, root string) {
	t.Helper()
	manifestDir := filepath.Join(root, ".caelis-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	manifest := `{
  "name":"Demo",
  "version":"1.2.3",
  "mcpServers":{"server":{"command":"demo-mcp"}}
}`
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func TestServiceLifecycleUsesHostState(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "demo")
	writeServiceTestPlugin(t, root)
	host := &memoryHost{
		dir: t.TempDir(),
		state: State{Plugins: []Config{{
			ID:      "demo",
			Root:    root,
			Enabled: false,
		}}},
		statuses: map[string][]mcp.MCPServerInfo{
			"demo": {{Name: "server", Status: "running", Tools: []string{"ping"}}},
		},
	}
	service := NewService(host)
	ctx := context.Background()

	listed, err := service.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	wantListed := []Info{{ID: "demo", Root: root, Status: "inactive"}}
	if !reflect.DeepEqual(listed, wantListed) {
		t.Fatalf("List() = %#v, want %#v", listed, wantListed)
	}

	enabled, err := service.Enable(ctx, " DEMO ")
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	wantEnabled := Info{
		ID:         "demo",
		Name:       "Demo",
		Version:    "1.2.3",
		Root:       root,
		Enabled:    true,
		MCPServers: []mcp.MCPServerInfo{{Name: "server", Status: "running", Tools: []string{"ping"}}},
		Status:     "active",
	}
	if !reflect.DeepEqual(enabled, wantEnabled) {
		t.Fatalf("Enable() = %#v, want %#v", enabled, wantEnabled)
	}

	disabled, err := service.Disable(ctx, "demo")
	if err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	wantDisabled := Info{
		ID:         "demo",
		Name:       "Demo",
		Version:    "1.2.3",
		Root:       root,
		MCPServers: []mcp.MCPServerInfo{{Name: "server", Status: "disabled"}},
		Status:     "inactive",
	}
	if !reflect.DeepEqual(disabled, wantDisabled) {
		t.Fatalf("Disable() = %#v, want %#v", disabled, wantDisabled)
	}

	wantMutations := []recordedMutation{
		{},
		{},
	}
	if !reflect.DeepEqual(host.mutations, wantMutations) {
		t.Fatalf("mutations = %#v, want %#v", host.mutations, wantMutations)
	}
	if host.loadCalls != 1 {
		t.Fatalf("LoadPluginState() calls = %d, want only the explicit List load", host.loadCalls)
	}
}

func TestServiceAddPathUsesHostState(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "demo")
	writeServiceTestPlugin(t, root)
	host := &memoryHost{dir: t.TempDir()}

	info, err := NewService(host).AddPath(context.Background(), root)
	if err != nil {
		t.Fatalf("AddPath() error = %v", err)
	}
	if info.ID != "demo" || !info.Enabled || info.Name != "Demo" {
		t.Fatalf("AddPath() = %#v", info)
	}
	if len(host.state.Plugins) != 1 || host.state.Plugins[0].Kind != ManifestKindCaelis {
		t.Fatalf("host state = %#v", host.state)
	}
	wantMutation := []recordedMutation{{}}
	if !reflect.DeepEqual(host.mutations, wantMutation) {
		t.Fatalf("mutations = %#v, want %#v", host.mutations, wantMutation)
	}
	if host.loadCalls != 0 {
		t.Fatalf("LoadPluginState() calls = %d, want committed mutation state", host.loadCalls)
	}
}

func TestServiceInstallCommitsBeforeManagedCacheGCWarning(t *testing.T) {
	storeDir := t.TempDir()
	marketplaceRoot := filepath.Join(storeDir, "plugins", "installed", "demo-cache", "revision-a", "marketplace")
	pluginRoot := filepath.Join(marketplaceRoot, "plugins", "demo")
	writeServiceTestPlugin(t, pluginRoot)
	manifestDir := filepath.Join(marketplaceRoot, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "marketplace.json"), []byte(`{
		"name":"demo-market",
		"plugins":[{"name":"demo","source":"./plugins/demo"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	host := &memoryHost{dir: storeDir, afterUpdate: cancel}
	info, err := NewService(host).Install(ctx, "demo@"+marketplaceRoot)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if info.ID != "demo" || !strings.Contains(info.Warning, context.Canceled.Error()) {
		t.Fatalf("Install() = %#v, want committed plugin with cache GC warning", info)
	}
	if len(host.state.Plugins) != 1 || host.state.Plugins[0].ID != "demo" {
		t.Fatalf("committed state = %#v, want demo plugin", host.state)
	}
}

func TestServiceRemoveIsConfigurationOnly(t *testing.T) {
	t.Parallel()

	storeDir := t.TempDir()
	outsideCache := filepath.Join(t.TempDir(), "managed-cache")
	if err := os.MkdirAll(filepath.Join(outsideCache, "plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	host := &memoryHost{
		dir: storeDir,
		state: State{Plugins: []Config{{
			ID:        "demo",
			Root:      filepath.Join(outsideCache, "plugin"),
			Managed:   true,
			CacheRoot: outsideCache,
		}}},
	}

	if err := NewService(host).Remove(context.Background(), "demo"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if len(host.state.Plugins) != 0 {
		t.Fatalf("host state = %#v, want committed removal", host.state)
	}
	if _, err := os.Stat(outsideCache); err != nil {
		t.Fatalf("managed cache was reclaimed during pure config Remove: %v", err)
	}
}

func TestServiceRequiresHost(t *testing.T) {
	t.Parallel()

	service := Service{}
	if _, err := service.List(context.Background()); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("List() error = %v, want ErrHostUnavailable", err)
	}
	if _, err := service.Enable(context.Background(), "demo"); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("Enable() error = %v, want ErrHostUnavailable", err)
	}
}

func TestInstallPropagatesMarketplaceStateLoadFailure(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("load failed")
	host := &memoryHost{dir: t.TempDir(), loadErr: sentinel}
	_, err := NewService(host).Install(context.Background(), "demo@market")
	if !errors.Is(err, sentinel) {
		t.Fatalf("Install() error = %v, want load failure", err)
	}
}
