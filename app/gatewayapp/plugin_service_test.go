package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/ports/gateway"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// helpers shared across plugin service tests

func buildMinimalPluginDir(t *testing.T, root string, manifestJSON string) {
	t.Helper()
	manifestDir := filepath.Join(root, ".caelis-plugin")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatalf("mkdir plugin manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte(manifestJSON), 0o600); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
}

func buildPluginStack(t *testing.T, storeDir, workspaceDir string) *Stack {
	t.Helper()
	cfg := Config{
		AppName:      "CAELIS",
		StoreDir:     storeDir,
		WorkspaceCWD: workspaceDir,
		SkillDirs:    []string{t.TempDir()},
		Sandbox:      SandboxConfig{RequestedType: "host"},
	}
	stack, err := NewLocalStack(cfg)
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	return stack
}

// TestPluginServiceAddPathHappyPath verifies that AddPath registers the plugin
// and persists the config so List and Inspect see it.
func TestPluginServiceAddPathHappyPath(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	pluginDir := filepath.Join(tmp, "myplugin")
	buildMinimalPluginDir(t, pluginDir, `{"name":"my-plugin","version":"1.2.3","description":"A test plugin"}`)

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	info, err := stack.Plugins().AddPath(ctx, pluginDir)
	if err != nil {
		t.Fatalf("AddPath() error = %v", err)
	}
	if info.ID != "myplugin" {
		t.Errorf("AddPath() ID = %q, want %q", info.ID, "myplugin")
	}
	if !info.Enabled {
		t.Error("AddPath() Enabled = false, want true")
	}
	if info.Status != "active" {
		t.Errorf("AddPath() Status = %q, want %q", info.Status, "active")
	}

	// Verify persistence: reload via List
	plugins, err := stack.Plugins().List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("List() len = %d, want 1", len(plugins))
	}
	if plugins[0].ID != "myplugin" {
		t.Errorf("List()[0].ID = %q, want %q", plugins[0].ID, "myplugin")
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load config after AddPath: %v", err)
	}
	if len(doc.Plugins) != 1 || doc.Plugins[0].Manifest == "" || doc.Plugins[0].Kind != "caelis" {
		t.Fatalf("persisted plugin manifest metadata = %+v, want caelis manifest metadata", doc.Plugins)
	}

	// Verify Inspect
	detail, err := stack.Plugins().Inspect(ctx, "myplugin")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if detail.Name != "my-plugin" {
		t.Errorf("Inspect().Name = %q, want %q", detail.Name, "my-plugin")
	}
	if detail.Version != "1.2.3" {
		t.Errorf("Inspect().Version = %q, want %q", detail.Version, "1.2.3")
	}
}

// TestPluginServiceEnableDisableHappyPath exercises Enable→Disable lifecycle.
func TestPluginServiceEnableDisableHappyPath(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	pluginDir := filepath.Join(tmp, "myplugin")
	buildMinimalPluginDir(t, pluginDir, `{"name":"my-plugin","version":"1.0.0","description":"A test plugin"}`)

	// Pre-seed config with plugin disabled
	configStore := newAppConfigStore(storeDir)
	if err := configStore.Save(AppConfig{
		Plugins: []PluginConfig{{ID: "myplugin", Root: pluginDir, Enabled: false}},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	// Enable
	info, err := stack.Plugins().Enable(ctx, "myplugin")
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if !info.Enabled {
		t.Error("Enable() Enabled = false, want true")
	}
	if info.Status != "active" {
		t.Errorf("Enable() Status = %q, want %q", info.Status, "active")
	}

	// Verify List shows active
	plugins, err := stack.Plugins().List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(plugins) != 1 || !plugins[0].Enabled {
		t.Errorf("List() after Enable: enabled = %v, want true", plugins[0].Enabled)
	}

	// Disable
	dinfo, err := stack.Plugins().Disable(ctx, "myplugin")
	if err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if dinfo.Enabled {
		t.Error("Disable() Enabled = true, want false")
	}
	if dinfo.Status != "inactive" {
		t.Errorf("Disable() Status = %q, want %q", dinfo.Status, "inactive")
	}

	// Verify List shows inactive
	plugins, err = stack.Plugins().List(ctx)
	if err != nil {
		t.Fatalf("List() after Disable error = %v", err)
	}
	if len(plugins) != 1 || plugins[0].Enabled {
		t.Errorf("List() after Disable: enabled = %v, want false", plugins[0].Enabled)
	}
}

func TestPluginServiceAddPathRefreshesSkillPrompt(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	pluginDir := filepath.Join(tmp, "skillplugin")
	skillDir := filepath.Join(pluginDir, "skills", "runtime-skill")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	buildMinimalPluginDir(t, pluginDir, `{"name":"skill-plugin","version":"1.0.0"}`)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: runtime-skill\ndescription: Runtime plugin skill.\n---\n# Runtime Skill\n"), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	systemPrompt, _ := stack.runtime.BaseMetadata["system_prompt"].(string)
	if strings.Contains(systemPrompt, "runtime-skill") {
		t.Fatalf("runtime-skill unexpectedly present before plugin add:\n%s", systemPrompt)
	}

	if _, err := stack.Plugins().AddPath(ctx, pluginDir); err != nil {
		t.Fatalf("AddPath() error = %v", err)
	}
	systemPrompt, _ = stack.runtime.BaseMetadata["system_prompt"].(string)
	if !strings.Contains(systemPrompt, "skillplugin:runtime-skill") {
		t.Fatalf("runtime-skill missing after plugin add without restart:\n%s", systemPrompt)
	}

	if _, err := stack.Plugins().Disable(ctx, "skillplugin"); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	systemPrompt, _ = stack.runtime.BaseMetadata["system_prompt"].(string)
	if strings.Contains(systemPrompt, "runtime-skill") {
		t.Fatalf("runtime-skill still present after plugin disable:\n%s", systemPrompt)
	}
}

// TestPluginServiceRemoveHappyPath verifies that Remove deletes the entry.
func TestPluginServiceRemoveHappyPath(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	pluginDir := filepath.Join(tmp, "myplugin")
	buildMinimalPluginDir(t, pluginDir, `{"name":"my-plugin","version":"1.0.0","description":""}`)

	configStore := newAppConfigStore(storeDir)
	if err := configStore.Save(AppConfig{
		Plugins: []PluginConfig{{ID: "myplugin", Root: pluginDir, Enabled: false}},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	if err := stack.Plugins().Remove(ctx, "myplugin"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	// List should now be empty
	plugins, err := stack.Plugins().List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("List() after Remove: len = %d, want 0", len(plugins))
	}

	// Inspect should return not-found
	_, err = stack.Plugins().Inspect(ctx, "myplugin")
	if err == nil || !strings.Contains(err.Error(), "plugin not found") {
		t.Errorf("Inspect() after Remove: err = %v, want 'plugin not found'", err)
	}
}

// TestPluginServiceEnableRollbackOnRebuildFailure verifies that if
// rebuildGateway fails after Enable (because the plugin directory was removed
// between registration and enable), the config is rolled back so that a
// subsequent List still shows the plugin as disabled.
func TestPluginServiceEnableRollbackOnRebuildFailure(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	pluginDir := filepath.Join(tmp, "brokenplug")
	buildMinimalPluginDir(t, pluginDir, `{"name":"broken-plug","version":"0.1.0"}`)

	// Seed config with plugin disabled
	configStore := newAppConfigStore(storeDir)
	if err := configStore.Save(AppConfig{
		Plugins: []PluginConfig{{ID: "brokenplug", Root: pluginDir, Enabled: false}},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	// Sabotage the plugin directory: delete it so rebuildGateway will fail
	// when it tries to parse the manifest.
	if err := os.RemoveAll(pluginDir); err != nil {
		t.Fatalf("remove plugin dir: %v", err)
	}

	// Enable should fail because rebuild cannot parse the missing directory.
	_, err := stack.Plugins().Enable(ctx, "brokenplug")
	if err == nil {
		t.Fatal("Enable() with missing plugin dir: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to enable plugin") {
		t.Errorf("Enable() error = %q, want 'failed to enable plugin'", err.Error())
	}

	// Config should have been rolled back: plugin still disabled.
	doc, loadErr := configStore.Load()
	if loadErr != nil {
		t.Fatalf("configStore.Load() error = %v", loadErr)
	}
	if len(doc.Plugins) != 1 {
		t.Fatalf("after rollback: plugin count = %d, want 1", len(doc.Plugins))
	}
	if doc.Plugins[0].Enabled {
		t.Error("after rollback: plugin is enabled, want disabled (rollback failed)")
	}
}

// TestPluginServiceAddPathRollbackOnRebuildFailure verifies that if AddPath
// succeeds at parsing but fails at rebuild (by using a manifest that parses
// OK the first time but causes rebuild to fail via a bad manifest written
// after the fact), the config is rolled back so List shows no new plugin.
//
// Because rebuildGateway re-reads the manifest from disk, we overwrite the
// manifest with invalid JSON after AddPath reads it but before rebuild. This
// requires us to interleave at the file level — we simulate it by registering
// a plugin from a separate valid directory and then using a directory whose
// manifest gets corrupted after parse.
//
// Note: AddPath calls ParsePlugin before acquiring the lock, then saves +
// rebuilds. We corrupt the manifest between the Parse and the Rebuild by
// writing a bad manifest synchronously; however since AddPath is sequential
// we instead test the scenario by pre-corrupting a manifest that parses OK
// initially via a sub-test that uses Disable rollback as a proxy.
//
// This test focuses on Remove rollback: register a good plugin, then make
// the directory disappear and verify that a second enabled plugin's rollback
// path works correctly (using Disable to remove and verifying state).
func TestPluginServiceRemoveRollbackOnRebuildFailure(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	goodDir := filepath.Join(tmp, "goodplugin")
	badDir := filepath.Join(tmp, "badplugin")
	buildMinimalPluginDir(t, goodDir, `{"name":"good","version":"1.0.0"}`)
	buildMinimalPluginDir(t, badDir, `{"name":"bad","version":"1.0.0"}`)

	// Both enabled initially
	configStore := newAppConfigStore(storeDir)
	if err := configStore.Save(AppConfig{
		Plugins: []PluginConfig{
			{ID: "goodplugin", Root: goodDir, Enabled: true},
			{ID: "badplugin", Root: badDir, Enabled: true},
		},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	// Sabotage badplugin by removing its directory. Since it is enabled,
	// any future rebuild will fail to parse it.
	if err := os.RemoveAll(badDir); err != nil {
		t.Fatalf("remove baddir: %v", err)
	}

	// Remove goodplugin — this should fail during rebuild because badplugin is broken
	err := stack.Plugins().Remove(ctx, "goodplugin")
	if err == nil {
		t.Fatal("Remove(goodplugin) expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to rebuild gateway after removing plugin") {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify rollback: goodplugin should still be in the config (and badplugin too)
	doc, loadErr := configStore.Load()
	if loadErr != nil {
		t.Fatalf("configStore.Load() error = %v", loadErr)
	}
	var foundGood, foundBad bool
	for _, p := range doc.Plugins {
		if p.ID == "goodplugin" {
			foundGood = true
		}
		if p.ID == "badplugin" {
			foundBad = true
		}
	}
	if !foundGood {
		t.Error("goodplugin was deleted from config, rollback failed")
	}
	if !foundBad {
		t.Error("badplugin was deleted from config, rollback failed")
	}
}

// TestPluginServiceListSkipsParseForDisabled verifies that disabled plugins
// do not trigger a parse error in List output.
func TestPluginServiceListSkipsParseForDisabled(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	// Disabled plugin with a missing root directory.
	configStore := newAppConfigStore(storeDir)
	if err := configStore.Save(AppConfig{
		Plugins: []PluginConfig{
			{ID: "ghost", Root: filepath.Join(tmp, "nonexistent"), Enabled: false, Name: "Ghost Plugin"},
		},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	plugins, err := stack.Plugins().List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v (disabled plugin with bad path should not fail list)", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("List() len = %d, want 1", len(plugins))
	}
	p := plugins[0]
	if p.ID != "ghost" {
		t.Errorf("List()[0].ID = %q, want %q", p.ID, "ghost")
	}
	// Status must be inactive, not error (we skip deep parse for disabled).
	if p.Status != "inactive" {
		t.Errorf("List()[0].Status = %q, want %q", p.Status, "inactive")
	}
	// Warning should be empty (no parse was attempted).
	if p.Warning != "" {
		t.Errorf("List()[0].Warning = %q, want empty", p.Warning)
	}
}

// TestPluginServiceNotFoundErrors verifies consistent "not found" errors.
func TestPluginServiceNotFoundErrors(t *testing.T) {
	t.Parallel()

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

	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{"Enable", func() error { _, err := stack.Plugins().Enable(ctx, "nosuch"); return err }},
		{"Disable", func() error { _, err := stack.Plugins().Disable(ctx, "nosuch"); return err }},
		{"Remove", func() error { return stack.Plugins().Remove(ctx, "nosuch") }},
		{"Inspect", func() error { _, err := stack.Plugins().Inspect(ctx, "nosuch"); return err }},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.fn()
			if err == nil {
				t.Fatalf("%s() with unknown id: expected error, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "plugin not found") {
				t.Errorf("%s() error = %q, want 'plugin not found'", tc.name, err.Error())
			}
		})
	}
}

// TestPluginServiceAddPathRejectsNonDirectory verifies AddPath rejects files.
func TestPluginServiceAddPathRejectsNonDirectory(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	aFile := filepath.Join(tmp, "notadir.txt")
	if err := os.WriteFile(aFile, []byte("hi"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	_, err := stack.Plugins().AddPath(context.Background(), aFile)
	if err == nil {
		t.Fatal("AddPath() with file path: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("AddPath() error = %q, want 'not a directory'", err.Error())
	}
}

func TestPluginServiceDisableRollbackOnRebuildFailure(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	goodDir := filepath.Join(tmp, "goodplugin")
	badDir := filepath.Join(tmp, "badplugin")
	buildMinimalPluginDir(t, goodDir, `{"name":"good","version":"1.0.0"}`)
	buildMinimalPluginDir(t, badDir, `{"name":"bad","version":"1.0.0"}`)

	// Both enabled initially
	configStore := newAppConfigStore(storeDir)
	if err := configStore.Save(AppConfig{
		Plugins: []PluginConfig{
			{ID: "goodplugin", Root: goodDir, Enabled: true},
			{ID: "badplugin", Root: badDir, Enabled: true},
		},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	// Sabotage badplugin
	if err := os.RemoveAll(badDir); err != nil {
		t.Fatalf("remove baddir: %v", err)
	}

	// Disable goodplugin — this should fail during rebuild because badplugin is broken
	_, err := stack.Plugins().Disable(ctx, "goodplugin")
	if err == nil {
		t.Fatal("Disable(goodplugin) expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to disable plugin") {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify rollback: goodplugin should still be enabled in the config
	doc, loadErr := configStore.Load()
	if loadErr != nil {
		t.Fatalf("configStore.Load() error = %v", loadErr)
	}
	for _, p := range doc.Plugins {
		if p.ID == "goodplugin" && !p.Enabled {
			t.Error("goodplugin was disabled in config, rollback failed")
		}
	}
}

func TestPluginServiceRejectsWhileActiveTurn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)

	blocking := &blockingRuntime{session: activeSession, release: make(chan struct{})}
	gw, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.Sessions,
		Runtime:  blocking,
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatalf("kernel.New() error = %v", err)
	}
	stack.gateway = gw

	handle, err := stack.currentGateway().BeginTurn(ctx, gateway.BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "hold active",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	defer handle.Handle.Close()
	defer func() {
		close(blocking.release)
		for range handle.Handle.ACPEvents() {
		}
	}()

	if got := len(stack.currentGateway().ActiveTurns()); got != 1 {
		t.Fatalf("ActiveTurns() len = %d, want 1", got)
	}

	// 1. AddPath should reject (pass an existing directory)
	existingDir := filepath.Join(t.TempDir(), "some-plugin")
	buildMinimalPluginDir(t, existingDir, `{"name":"existing","version":"1.0.0"}`)
	_, err = stack.Plugins().AddPath(ctx, existingDir)
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Errorf("expected active turn error for AddPath, got: %v", err)
	}

	// 2. Enable should reject
	_, err = stack.Plugins().Enable(ctx, "some-plugin")
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Errorf("expected active turn error for Enable, got: %v", err)
	}

	// 3. Disable should reject
	_, err = stack.Plugins().Disable(ctx, "some-plugin")
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Errorf("expected active turn error for Disable, got: %v", err)
	}

	// 4. Remove should reject
	err = stack.Plugins().Remove(ctx, "some-plugin")
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Errorf("expected active turn error for Remove, got: %v", err)
	}
}

func TestPluginServiceRollbackFailurePath(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	goodDir := filepath.Join(tmp, "goodplugin")
	buildMinimalPluginDir(t, goodDir, `{"name":"good","version":"1.0.0"}`)

	configStore := newAppConfigStore(storeDir)
	if err := configStore.Save(AppConfig{
		Plugins: []PluginConfig{
			{ID: "goodplugin", Root: goodDir, Enabled: false},
		},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	// Sabotage goodDir directory so Enable will fail during rebuild
	if err := os.RemoveAll(goodDir); err != nil {
		t.Fatalf("remove goodDir: %v", err)
	}

	// Inject a saveHook to fail only on the rollback (second) save call
	var saveCount int
	sentinelErr := errors.New("injected save error")
	stack.store.saveHook = func(doc AppConfig) error {
		saveCount++
		if saveCount == 2 {
			return sentinelErr
		}
		return nil
	}

	// Enable should fail and return an error containing rollback failure details
	_, err := stack.Plugins().Enable(ctx, "goodplugin")
	if err == nil {
		t.Fatal("Enable() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rollback config save failed") {
		t.Errorf("expected 'rollback config save failed' error, got: %v", err)
	}
	if !errors.Is(err, sentinelErr) {
		t.Errorf("expected error to wrap sentinel error %q, but it did not: %v", sentinelErr, err)
	}
}

func TestGatewayMCPServerHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_GATEWAY_MCP_HELPER") != "1" {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	mcpsdk.AddTool[any, any](server, &mcpsdk.Tool{
		Name:        "ping",
		Description: "Pings",
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ any) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "pong"}},
		}, nil, nil
	})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestPluginServiceMCPServers(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	_ = os.MkdirAll(storeDir, 0o700)
	_ = os.MkdirAll(workspaceDir, 0o700)

	manifestJSON := fmt.Sprintf(`{
		"name": "mcp-plugin",
		"version": "1.0.0",
		"mcpServers": {
			"myserver": {
				"command": %q,
				"args": ["-test.run=^TestGatewayMCPServerHelperProcess$"],
				"env": {
					"CAELIS_GATEWAY_MCP_HELPER": "1"
				}
			}
		}
	}`, os.Args[0])

	pluginDir := filepath.Join(tmp, "myplugin")
	buildMinimalPluginDir(t, pluginDir, manifestJSON)

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	info, err := stack.Plugins().AddPath(ctx, pluginDir)
	if err != nil {
		t.Fatalf("AddPath() failed: %v", err)
	}

	if len(info.MCPServers) != 1 {
		t.Fatalf("expected 1 MCP server, got %d", len(info.MCPServers))
	}
	mcpSrv := info.MCPServers[0]
	if mcpSrv.Name != "myserver" {
		t.Errorf("expected server name 'myserver', got %s", mcpSrv.Name)
	}
	if mcpSrv.Status != "running" {
		t.Errorf("expected status 'running', got %s", mcpSrv.Status)
	}
	if len(mcpSrv.Tools) != 1 || mcpSrv.Tools[0] != "ping" {
		t.Errorf("expected tool list ['ping'], got %v", mcpSrv.Tools)
	}

	detail, err := stack.Plugins().Inspect(ctx, "myplugin")
	if err != nil {
		t.Fatalf("Inspect() failed: %v", err)
	}
	if len(detail.MCPServers) != 1 || detail.MCPServers[0].Status != "running" {
		t.Errorf("expected running MCP server in inspect, got: %+v", detail.MCPServers)
	}
}

func TestPluginServiceAgentContributions(t *testing.T) {
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "ws")
	_ = os.MkdirAll(storeDir, 0o700)
	_ = os.MkdirAll(workspaceDir, 0o700)

	pluginDir := filepath.Join(tmp, "agentplugin")
	if err := os.MkdirAll(filepath.Join(pluginDir, ".caelis-plugin"), 0o700); err != nil {
		t.Fatalf("mkdir caelis manifest dir: %v", err)
	}
	manifestJSON := fmt.Sprintf(`{
		"name": "agent-plugin",
		"version": "1.0.0",
		"agents": [
			{
				"name": "plugin-helper",
				"description": "Plugin helper agent",
				"command": %q,
				"args": ["-test.run=^TestGatewayMCPServerHelperProcess$"],
				"env": {"PLUGIN_AGENT_TEST": "1"}
			}
		]
	}`, os.Args[0])
	if err := os.WriteFile(filepath.Join(pluginDir, ".caelis-plugin", "plugin.json"), []byte(manifestJSON), 0o600); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}

	stack := buildPluginStack(t, storeDir, workspaceDir)
	ctx := context.Background()

	info, err := stack.Plugins().AddPath(ctx, pluginDir)
	if err != nil {
		t.Fatalf("AddPath() failed: %v", err)
	}
	if len(info.Agents) != 1 || info.Agents[0] != "plugin-helper" {
		t.Fatalf("AddPath() agents = %#v, want plugin-helper", info.Agents)
	}

	agent, ok := agentConfigByNameForPluginTest(stack.runtime.Assembly.Agents, "plugin-helper")
	if !ok {
		t.Fatalf("plugin-helper missing from runtime assembly: %#v", stack.runtime.Assembly.Agents)
	}
	if agent.Command != os.Args[0] {
		t.Fatalf("plugin-helper command = %q, want %q", agent.Command, os.Args[0])
	}
	if got := agent.Env["PLUGIN_AGENT_TEST"]; got != "1" {
		t.Fatalf("plugin-helper env PLUGIN_AGENT_TEST = %q, want 1", got)
	}
	if agent.WorkDir != pluginDir {
		t.Fatalf("plugin-helper workdir = %q, want plugin dir %q", agent.WorkDir, pluginDir)
	}

	detail, err := stack.Plugins().Inspect(ctx, "agentplugin")
	if err != nil {
		t.Fatalf("Inspect() failed: %v", err)
	}
	if len(detail.Agents) != 1 || detail.Agents[0] != "plugin-helper" {
		t.Fatalf("Inspect() agents = %#v, want plugin-helper", detail.Agents)
	}

	if _, err := stack.Plugins().Disable(ctx, "agentplugin"); err != nil {
		t.Fatalf("Disable() failed: %v", err)
	}
	if _, ok := agentConfigByNameForPluginTest(stack.runtime.Assembly.Agents, "plugin-helper"); ok {
		t.Fatalf("plugin-helper still present after disable: %#v", stack.runtime.Assembly.Agents)
	}
}

func agentConfigByNameForPluginTest(agents []assembly.AgentConfig, name string) (assembly.AgentConfig, bool) {
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), name) {
			return agent, true
		}
	}
	return assembly.AgentConfig{}, false
}
