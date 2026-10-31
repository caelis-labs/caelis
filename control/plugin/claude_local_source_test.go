package plugin

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLocalClaudePluginUsesManifestIdentityAndRetainsSource(t *testing.T) {
	root := filepath.Join(t.TempDir(), "桌面 plugin 0.2.0 darwin-arm64")
	manifest := `{"name":"desktop-world","version":"0.2.0"}`
	writeAgentPluginFile(t, root, ".claude-plugin/plugin.json", manifest)
	writeAgentPluginFile(t, root, "skills/desktop-world/SKILL.md", "---\nname: desktop-world\ndescription: Desktop actions\n---\nUse app and source.\n")
	host := &memoryHost{dir: t.TempDir()}
	service := NewService(host)
	installed, err := service.Install(t.Context(), root)
	if err != nil || installed.ID != "desktop-world" {
		t.Fatalf("manifest identity = %+v, %v", installed, err)
	}
	if _, err := service.Disable(t.Context(), installed.ID); err != nil {
		t.Fatal(err)
	}
	host.state.Plugins[0].ExecutableEnv = map[string]string{"NODE": "/trusted/node"}
	before := host.state.Clone()
	writeAgentPluginFile(t, root, ".claude-plugin/plugin.json", strings.Replace(manifest, "0.2.0", "0.2.1", 1))
	repeated, err := service.Install(t.Context(), root)
	if err != nil || repeated.ID != installed.ID || repeated.Enabled || repeated.Version != "0.2.1" {
		t.Fatalf("same-source upgrade = %+v, %v", repeated, err)
	}
	if len(host.state.Plugins) != 1 || !reflect.DeepEqual(host.state.Plugins[0].ExecutableEnv, before.Plugins[0].ExecutableEnv) {
		t.Fatal("same-source upgrade lost Host configuration")
	}
	before = host.state.Clone()
	other := filepath.Join(t.TempDir(), "another desktop-world package")
	writeAgentPluginFile(t, other, ".claude-plugin/plugin.json", manifest)
	if _, err := service.Install(t.Context(), other); err == nil || !strings.Contains(err.Error(), "another source") {
		t.Fatalf("different-source collision = %v", err)
	}
	if !reflect.DeepEqual(host.state, before) {
		t.Fatal("different source replaced registered plugin")
	}
}

func TestLocalClaudePluginPinsCanonicalSource(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	for _, dir := range []string{root, other} {
		writeAgentPluginFile(t, dir, ".claude-plugin/plugin.json", `{"name":"desktop-world","version":"0.2.0"}`)
	}
	link := filepath.Join(t.TempDir(), "desktop-world alias")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	host := &memoryHost{dir: t.TempDir()}
	service := NewService(host)
	installed, err := service.Install(t.Context(), link)
	if err != nil || installed.ID != "desktop-world" || installed.Root != wantRoot {
		t.Fatalf("canonical install = %+v, %v", installed, err)
	}
	if host.state.Plugins[0].Manifest != filepath.Join(wantRoot, ".claude-plugin/plugin.json") {
		t.Fatalf("manifest not pinned: %q", host.state.Plugins[0].Manifest)
	}
	if _, err := service.Install(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	before := host.state.Clone()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Install(t.Context(), link); err == nil {
		t.Fatal("retargeted symlink replaced registered source")
	}
	if !reflect.DeepEqual(host.state, before) {
		t.Fatal("retargeting changed registration")
	}
}

func TestLocalClaudePluginRequiresExplicitLegacyIDMigration(t *testing.T) {
	root := t.TempDir()
	writeAgentPluginFile(t, root, ".claude-plugin/plugin.json", `{"name":"desktop-world","version":"0.2.0"}`)
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	host := &memoryHost{dir: t.TempDir(), state: State{Plugins: []Config{{
		ID: "desktop-world-0.2.0-darwin-arm64", Name: "desktop-world", Kind: ManifestKindClaude,
		Root: canonical, Manifest: filepath.Join(canonical, ".claude-plugin/plugin.json"), Enabled: false,
		ExecutableEnv: map[string]string{"NODE": "/trusted/node"},
	}}}}
	before := host.state.Clone()
	if _, err := NewService(host).Install(t.Context(), root); err == nil || !strings.Contains(err.Error(), "already registered as") || !strings.Contains(err.Error(), "remove") {
		t.Fatalf("legacy ID migration needs explicit removal: %v", err)
	}
	if !reflect.DeepEqual(host.state, before) {
		t.Fatal("legacy registration was silently duplicated or renamed")
	}
}
