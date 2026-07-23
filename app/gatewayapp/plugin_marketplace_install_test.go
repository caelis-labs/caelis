package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type marketplaceInstallFixture struct {
	MarketplaceName string
	PluginName      string
	SourceDir       string
	ManifestName    string
	Description     string
	SkillName       string
}

func buildMarketplaceInstallFixture(t *testing.T, tmp string, fixture marketplaceInstallFixture) (string, string) {
	t.Helper()
	marketplaceName := firstNonEmpty(fixture.MarketplaceName, "local-marketplace")
	pluginName := firstNonEmpty(fixture.PluginName, "demo-plugin")
	sourceDir := firstNonEmpty(fixture.SourceDir, pluginName)
	manifestName := firstNonEmpty(fixture.ManifestName, pluginName)
	description := firstNonEmpty(fixture.Description, "Demo plugin")

	marketplaceDir := filepath.Join(tmp, "marketplace")
	pluginDir := filepath.Join(marketplaceDir, "plugins", sourceDir)
	manifestDir := filepath.Join(pluginDir, ".claude-plugin")
	marketplaceManifestDir := filepath.Join(marketplaceDir, ".claude-plugin")
	dirs := []string{manifestDir, marketplaceManifestDir}
	if fixture.SkillName != "" {
		dirs = append(dirs, filepath.Join(pluginDir, "skills", fixture.SkillName))
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte(`{
		"name": "`+manifestName+`",
		"version": "1.0.0",
		"description": "`+description+`"
	}`), 0o600); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	if fixture.SkillName != "" {
		skillPath := filepath.Join(pluginDir, "skills", fixture.SkillName, "SKILL.md")
		if err := os.WriteFile(skillPath, []byte("---\nname: "+fixture.SkillName+"\ndescription: "+description+".\n---\n# "+fixture.SkillName+"\n"), 0o600); err != nil {
			t.Fatalf("write plugin skill: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(marketplaceManifestDir, "marketplace.json"), []byte(`{
		"name": "`+marketplaceName+`",
		"plugins": [
			{
				"name": "`+pluginName+`",
				"description": "`+description+`",
				"source": "./plugins/`+sourceDir+`"
			}
		]
	}`), 0o600); err != nil {
		t.Fatalf("write marketplace manifest: %v", err)
	}
	return marketplaceDir, pluginDir
}

func TestPluginServiceInstallFromClaudeMarketplaceDirectory(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	marketplaceDir, pluginDir := buildMarketplaceInstallFixture(t, tmp, marketplaceInstallFixture{
		PluginName:   "mcp-server-dev",
		ManifestName: "MCP Server Dev",
		Description:  "Build MCP servers",
	})

	stack := buildPluginStack(t, storeDir, workspaceDir)
	info, err := stack.Plugins().Install(context.Background(), "mcp-server-dev@"+marketplaceDir)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if info.ID != "mcp-server-dev" || !info.Enabled {
		t.Fatalf("Install() = %#v, want enabled mcp-server-dev", info)
	}
	if info.Name != "MCP Server Dev" || info.Status != "active" {
		t.Fatalf("Install() = %#v, want manifest details and active status", info)
	}
	if info.Root != pluginDir {
		t.Fatalf("Install() root = %q, want %q", info.Root, pluginDir)
	}
}

func TestPluginServiceInstallFromMarketplaceUsesEntryNameWhenSourceDirDiffers(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	marketplaceDir, pluginDir := buildMarketplaceInstallFixture(t, tmp, marketplaceInstallFixture{
		MarketplaceName: "drawio",
		PluginName:      "drawio",
		SourceDir:       "claude-code",
		ManifestName:    "drawio",
		Description:     "Draw diagrams",
		SkillName:       "drawio",
	})

	stack := buildPluginStack(t, storeDir, workspaceDir)
	info, err := stack.Plugins().Install(context.Background(), "drawio@"+marketplaceDir)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if info.ID != "drawio" || info.Root != pluginDir {
		t.Fatalf("Install() = %#v, want drawio ID from marketplace entry and root %q", info, pluginDir)
	}
	if !slices.Contains(info.Skills, "drawio:drawio") {
		t.Fatalf("Install() skills = %#v, want drawio namespace", info.Skills)
	}

	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(doc.Plugins) != 1 || doc.Plugins[0].ID != "drawio" {
		t.Fatalf("persisted plugins = %#v, want one drawio plugin", doc.Plugins)
	}
	if len(stack.runtime.PluginSkills) != 1 ||
		stack.runtime.PluginSkills[0].Plugin != "drawio" ||
		stack.runtime.PluginSkills[0].Namespace != "drawio" {
		t.Fatalf("runtime plugin skills = %#v, want drawio identity", stack.runtime.PluginSkills)
	}
}

func TestPluginServiceMarketplaceInstallRenamesExistingSameRoot(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	marketplaceDir, pluginDir := buildMarketplaceInstallFixture(t, tmp, marketplaceInstallFixture{
		MarketplaceName: "drawio",
		PluginName:      "drawio",
		SourceDir:       "claude-code",
		ManifestName:    "drawio",
		Description:     "Draw diagrams",
		SkillName:       "drawio",
	})

	configStore := newAppConfigStore(storeDir)
	if err := configStore.Save(AppConfig{
		Plugins: []PluginConfig{{
			ID:      "claude-code",
			Name:    "drawio",
			Root:    pluginDir,
			Enabled: true,
		}},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	info, err := stack.Plugins().Install(context.Background(), "drawio@"+marketplaceDir)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if info.ID != "drawio" {
		t.Fatalf("Install() ID = %q, want drawio", info.ID)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(doc.Plugins) != 1 || doc.Plugins[0].ID != "drawio" || doc.Plugins[0].Root != pluginDir {
		t.Fatalf("persisted plugins = %#v, want renamed drawio entry", doc.Plugins)
	}
	if _, err := stack.Plugins().Inspect(context.Background(), "claude-code"); err == nil {
		t.Fatal("Inspect(claude-code) error = nil, want old ID removed")
	}
}

func TestPluginServiceMarketplaceInstallUpdatesSameIDAtNewRoot(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	marketplaceDir, pluginDir := buildMarketplaceInstallFixture(t, tmp, marketplaceInstallFixture{
		MarketplaceName: "drawio",
		PluginName:      "drawio",
		SourceDir:       "claude-code-v2",
		ManifestName:    "drawio",
		Description:     "Draw diagrams",
	})
	oldDir := filepath.Join(tmp, "old-drawio")
	buildMinimalPluginDir(t, oldDir, `{"name":"old-drawio","version":"1.0.0"}`)

	configStore := newAppConfigStore(storeDir)
	if err := configStore.Save(AppConfig{
		Plugins: []PluginConfig{{
			ID:      "drawio",
			Name:    "old-drawio",
			Root:    oldDir,
			Enabled: true,
		}},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	info, err := stack.Plugins().Install(context.Background(), "drawio@"+marketplaceDir)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if info.ID != "drawio" || info.Root != pluginDir {
		t.Fatalf("Install() = %#v, want same ID updated to root %q", info, pluginDir)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(doc.Plugins) != 1 || doc.Plugins[0].ID != "drawio" || doc.Plugins[0].Root != pluginDir {
		t.Fatalf("persisted plugins = %#v, want drawio updated to new root", doc.Plugins)
	}
}

func TestPluginServiceMarketplaceInstallRejectsIDCollisionOnRootRename(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	marketplaceDir, pluginDir := buildMarketplaceInstallFixture(t, tmp, marketplaceInstallFixture{
		MarketplaceName: "drawio",
		PluginName:      "drawio",
		SourceDir:       "claude-code",
		ManifestName:    "drawio",
		Description:     "Draw diagrams",
	})
	otherDir := filepath.Join(tmp, "other-drawio")
	buildMinimalPluginDir(t, otherDir, `{"name":"other-drawio","version":"1.0.0"}`)

	configStore := newAppConfigStore(storeDir)
	if err := configStore.Save(AppConfig{
		Plugins: []PluginConfig{
			{ID: "drawio", Name: "other-drawio", Root: otherDir, Enabled: true},
			{ID: "claude-code", Name: "drawio", Root: pluginDir, Enabled: true},
		},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	_, err := stack.Plugins().Install(context.Background(), "drawio@"+marketplaceDir)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Install() error = %v, want ID collision", err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(doc.Plugins) != 2 ||
		doc.Plugins[0].ID != "drawio" || doc.Plugins[0].Root != otherDir ||
		doc.Plugins[1].ID != "claude-code" || doc.Plugins[1].Root != pluginDir {
		t.Fatalf("plugins mutated after collision: %#v", doc.Plugins)
	}
}
