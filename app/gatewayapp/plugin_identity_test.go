package gatewayapp

import (
	"os"
	"path/filepath"
	"testing"

	pluginapi "github.com/caelis-labs/caelis/ports/plugin"
)

func TestPluginConfigID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		override string
		want     string
	}{
		{name: "path fallback", path: "/tmp/MyPlugin", want: "myplugin"},
		{name: "override wins", path: "/tmp/claude-code", override: " DrawIO ", want: "drawio"},
		{name: "blank override falls back", path: "/tmp/claude-code", override: " ", want: "claude-code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := pluginConfigID(tt.path, tt.override); got != tt.want {
				t.Fatalf("pluginConfigID(%q, %q) = %q, want %q", tt.path, tt.override, got, tt.want)
			}
		})
	}
}

func TestSamePluginRoot(t *testing.T) {
	t.Parallel()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if !samePluginRoot(".", cwd) {
		t.Fatalf("samePluginRoot(%q, %q) = false, want true", ".", cwd)
	}

	root := t.TempDir()
	if samePluginRoot("", root) {
		t.Fatalf("samePluginRoot(empty, %q) = true, want false", root)
	}
	if samePluginRoot(root, filepath.Join(t.TempDir(), "missing")) {
		t.Fatalf("samePluginRoot(root, missing sibling) = true, want false")
	}

	link := filepath.Join(t.TempDir(), "plugin-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if !samePluginRoot(root, link) {
		t.Fatalf("samePluginRoot(%q, symlink %q) = false, want true", root, link)
	}
}

func TestUpsertLocalPluginConfigMatchesByIDOnly(t *testing.T) {
	t.Parallel()

	otherRoot := filepath.Join(t.TempDir(), "other")
	pluginRoot := filepath.Join(t.TempDir(), "plugin")
	doc := AppConfig{Plugins: []PluginConfig{
		{ID: "drawio", Root: otherRoot, Name: "other"},
		{ID: "claude-code", Root: pluginRoot, Name: "old"},
	}}
	next := PluginConfig{ID: "drawio", Root: pluginRoot, Name: "new"}

	upsertLocalPluginConfig(&doc, next)

	if len(doc.Plugins) != 2 {
		t.Fatalf("plugins = %#v, want two entries", doc.Plugins)
	}
	if doc.Plugins[0].Root != pluginRoot || doc.Plugins[0].Name != "new" {
		t.Fatalf("first plugin = %#v, want ID match updated", doc.Plugins[0])
	}
	if doc.Plugins[1].ID != "claude-code" || doc.Plugins[1].Root != pluginRoot {
		t.Fatalf("second plugin = %#v, want root match ignored", doc.Plugins[1])
	}
}

func TestUpsertMarketplacePluginConfigMatchesRootBeforeID(t *testing.T) {
	t.Parallel()

	otherRoot := filepath.Join(t.TempDir(), "other")
	pluginRoot := filepath.Join(t.TempDir(), "plugin")
	doc := AppConfig{Plugins: []PluginConfig{
		{ID: "unrelated", Root: otherRoot, Name: "other"},
		{ID: "claude-code", Root: pluginRoot, Name: "old"},
	}}
	next := PluginConfig{ID: "drawio", Root: pluginRoot, Name: "new"}

	if err := upsertMarketplacePluginConfig(&doc, next); err != nil {
		t.Fatalf("upsertMarketplacePluginConfig() error = %v", err)
	}
	if len(doc.Plugins) != 2 {
		t.Fatalf("plugins = %#v, want two entries", doc.Plugins)
	}
	if doc.Plugins[0].ID != "unrelated" || doc.Plugins[0].Root != otherRoot {
		t.Fatalf("first plugin = %#v, want unrelated entry unchanged", doc.Plugins[0])
	}
	if doc.Plugins[1].ID != "drawio" || doc.Plugins[1].Root != pluginRoot {
		t.Fatalf("second plugin = %#v, want root match renamed to drawio", doc.Plugins[1])
	}
}

func TestUpsertMarketplacePluginConfigRejectsIDCollision(t *testing.T) {
	t.Parallel()

	otherRoot := filepath.Join(t.TempDir(), "other")
	pluginRoot := filepath.Join(t.TempDir(), "plugin")
	doc := AppConfig{Plugins: []PluginConfig{
		{ID: "drawio", Root: otherRoot, Name: "other"},
		{ID: "claude-code", Root: pluginRoot, Name: "old"},
	}}
	next := PluginConfig{ID: "drawio", Root: pluginRoot, Name: "new"}

	if err := upsertMarketplacePluginConfig(&doc, next); err == nil {
		t.Fatal("upsertMarketplacePluginConfig() error = nil, want ID collision")
	}
	if doc.Plugins[0].Root != otherRoot || doc.Plugins[1].ID != "claude-code" {
		t.Fatalf("plugins mutated after collision: %#v", doc.Plugins)
	}
}

func TestUpsertMarketplacePluginConfigUpdatesSameIDAtNewRoot(t *testing.T) {
	t.Parallel()

	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	doc := AppConfig{Plugins: []PluginConfig{
		{ID: "drawio", Root: oldRoot, Name: "old"},
	}}
	next := PluginConfig{ID: "drawio", Root: newRoot, Name: "new"}

	if err := upsertMarketplacePluginConfig(&doc, next); err != nil {
		t.Fatalf("upsertMarketplacePluginConfig() error = %v", err)
	}
	if len(doc.Plugins) != 1 || doc.Plugins[0].ID != "drawio" || doc.Plugins[0].Root != newRoot || doc.Plugins[0].Name != "new" {
		t.Fatalf("plugins = %#v, want same ID updated to new root", doc.Plugins)
	}
}

func TestPluginWithConfiguredIDRewritesRuntimeContributionIDs(t *testing.T) {
	t.Parallel()

	p := pluginapi.InstalledPlugin{
		ID: "claude-code",
		Skills: []pluginapi.SkillContribution{
			{
				Namespace: "claude-code",
				Root:      "/tmp/plugin/skills",
				Disabled:  []string{"claude-code:draw", "draw-local", "custom:keep"},
			},
			{
				Namespace: "custom",
				Root:      "/tmp/plugin/custom-skills",
				Disabled:  []string{"claude-code:custom-local"},
			},
		},
		Hooks: []pluginapi.HookSpec{{
			PluginID: "claude-code",
			Event:    pluginapi.HookEventSessionStart,
		}},
		MCPServers: []pluginapi.MCPServerSpec{{
			PluginID: "claude-code",
			Name:     "drawio",
		}},
	}

	got := pluginWithConfiguredID(p, "drawio")
	if got.ID != "drawio" {
		t.Fatalf("ID = %q, want drawio", got.ID)
	}
	if got.Skills[0].Namespace != "drawio" {
		t.Fatalf("default skill namespace = %q, want drawio", got.Skills[0].Namespace)
	}
	if got.Skills[0].Disabled[0] != "drawio:draw" || got.Skills[0].Disabled[1] != "draw-local" || got.Skills[0].Disabled[2] != "custom:keep" {
		t.Fatalf("default skill disabled refs = %#v, want old namespace rewritten only", got.Skills[0].Disabled)
	}
	if got.Skills[1].Namespace != "custom" {
		t.Fatalf("explicit skill namespace = %q, want custom", got.Skills[1].Namespace)
	}
	if got.Skills[1].Disabled[0] != "claude-code:custom-local" {
		t.Fatalf("custom skill disabled refs = %#v, want custom namespace left untouched", got.Skills[1].Disabled)
	}
	if got.Hooks[0].PluginID != "drawio" {
		t.Fatalf("hook PluginID = %q, want drawio", got.Hooks[0].PluginID)
	}
	if got.MCPServers[0].PluginID != "drawio" {
		t.Fatalf("mcp PluginID = %q, want drawio", got.MCPServers[0].PluginID)
	}
}
