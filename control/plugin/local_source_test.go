package plugin

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLocalAgentPluginSourceOwnershipAndUpgrade(t *testing.T) {
	ctx := context.Background()
	host := &memoryHost{dir: t.TempDir(), state: State{
		Plugins:      []Config{{ID: "unrelated", Name: "keep", Enabled: false, ExecutableEnv: map[string]string{"NODE": "/trusted/node"}}},
		Marketplaces: []MarketplaceConfig{{Name: "keep-market", Source: "keep-source"}},
	}}
	service := NewService(host)
	root := t.TempDir()
	manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"desktop-world","version":"1.0.0","author":{"name":"Caelis Labs","url":"https://github.com/caelis-labs"}}`
	writeAgentPluginFile(t, root, "plugin.json", manifest)
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Install(ctx, root); err != nil {
		t.Fatal(err)
	}
	host.state.Plugins[1].ExecutableEnv = map[string]string{"DTW_NODE_PATH": "/trusted/node"}
	if _, err := service.Disable(ctx, "desktop-world"); err != nil {
		t.Fatal(err)
	}
	before := host.state.Clone()
	if info, err := service.Install(ctx, root); err != nil || info.Enabled {
		t.Fatalf("repeat = %+v, %v", info, err)
	}
	if !reflect.DeepEqual(before, host.state) {
		t.Fatalf("repeat changed plugin settings: %+v", host.state)
	}
	writeAgentPluginFile(t, root, "plugin.json", strings.Replace(manifest, `"version":"1.0.0"`, `"version":"2.0.0"`, 1))
	info, err := service.Install(ctx, root)
	if err != nil || info.Version != "2.0.0" || info.Root != wantRoot || info.Enabled {
		t.Fatalf("same-source upgrade = %+v, %v", info, err)
	}
	if !reflect.DeepEqual(before.Plugins[0], host.state.Plugins[0]) || !reflect.DeepEqual(before.Marketplaces, host.state.Marketplaces) || !reflect.DeepEqual(before.Plugins[1].ExecutableEnv, host.state.Plugins[1].ExecutableEnv) {
		t.Fatalf("upgrade lost unrelated state or executable selection: %+v", host.state)
	}
	before = host.state.Clone()
	for _, publisher := range []string{"Caelis Labs", "Another publisher"} {
		other := t.TempDir()
		writeAgentPluginFile(t, other, "plugin.json", strings.Replace(manifest, "Caelis Labs", publisher, 1))
		if _, err := service.Install(ctx, other); err == nil || !strings.Contains(err.Error(), "another source") {
			t.Fatalf("different-source collision = %v", err)
		}
		if !reflect.DeepEqual(before, host.state) {
			t.Fatalf("collision changed state: %+v", host.state)
		}
	}
}

func TestLocalAgentPluginPinsCanonicalSource(t *testing.T) {
	ctx := context.Background()
	host := &memoryHost{dir: t.TempDir()}
	service := NewService(host)
	manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo"}`
	root, other := t.TempDir(), t.TempDir()
	for _, dir := range []string{root, other} {
		writeAgentPluginFile(t, dir, "plugin.json", manifest)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "plugin-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	installed, err := service.Install(ctx, link)
	if err != nil || installed.Root != wantRoot {
		t.Fatalf("canonical install = %+v, %v", installed, err)
	}
	if _, err := service.Install(ctx, root); err != nil {
		t.Fatalf("canonical repeat: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Install(ctx, link); err == nil {
		t.Fatal("retargeted symlink replaced the registered source")
	}
}

func TestLocalAgentPluginDoesNotReplaceLegacyOrManagedPlugin(t *testing.T) {
	for _, kind := range []ManifestKind{ManifestKindCaelis, ManifestKindClaude, ManifestKindAgentPlugin} {
		t.Run(string(kind), func(t *testing.T) {
			root := t.TempDir()
			writeAgentPluginFile(t, root, "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo"}`)
			host := &memoryHost{dir: t.TempDir(), state: State{Plugins: []Config{{ID: "demo", Kind: kind, Root: root, Managed: kind == ManifestKindAgentPlugin}}}}
			before := host.state.Clone()
			if _, err := NewService(host).Install(context.Background(), root); err == nil {
				t.Fatal("installation replaced a different registration owner")
			}
			if !reflect.DeepEqual(before, host.state) {
				t.Fatal("collision mutated existing registration")
			}
		})
	}
}
