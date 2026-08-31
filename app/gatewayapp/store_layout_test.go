package gatewayapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func TestMigrateRetiredStoreLayoutUsesBuiltInCodexAndReclaimsCache(t *testing.T) {
	storeDir := t.TempDir()
	legacyRoot := filepath.Join(storeDir, legacyACPAgentDirectory)
	launcher := filepath.Join(legacyRoot, "npm", "installations", "codex-acp", "1.1.7", "node_modules", ".bin", "codex-acp")
	if err := os.MkdirAll(filepath.Dir(launcher), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range retiredRootStoreFiles {
		if err := os.WriteFile(filepath.Join(storeDir, name), []byte("retired"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := newAppConfigStore(storeDir)
	doc := AppConfig{
		SchemaVersion: 2,
		ExternalAgents: controlagents.Configuration{
			Connections: []controlagents.Connection{{
				ID: "codex", Name: "Codex", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindManaged, Command: launcher},
			}},
			Agents:      []controlagents.Agent{{ID: "codex-agent", Name: "Codex", ConnectionID: "codex"}},
			Discoveries: []controlagents.DiscoverySnapshot{{ConnectionID: "codex", LaunchFingerprint: "legacy"}},
		},
	}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	doc, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := migrateRetiredStoreLayout(context.Background(), store, storeDir, doc, true)
	if err != nil {
		t.Fatal(err)
	}
	preparations, err := newACPPreparationStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer preparations.Close()
	if err := reclaimRetiredACPAgentStore(context.Background(), storeDir, migrated, preparations); err != nil {
		t.Fatal(err)
	}
	connection, ok := controlagents.LookupConnection(migrated.ExternalAgents, "codex")
	if !ok || connection.Name != "Codex" || connection.Launcher.Kind != controlagents.LaunchKindHostedAdapter || connection.Launcher.AdapterID != "codex" || connection.Launcher.Command != "" {
		t.Fatalf("migrated connection = %#v", connection)
	}
	if agent, ok := controlagents.LookupAgent(migrated.ExternalAgents, "codex-agent"); !ok || agent.ConnectionID != "codex" {
		t.Fatalf("stable Agent identity was not preserved: %#v", migrated.ExternalAgents.Agents)
	}
	if len(migrated.ExternalAgents.Discoveries) != 0 {
		t.Fatalf("stale discoveries = %#v", migrated.ExternalAgents.Discoveries)
	}
	if _, err := os.Stat(legacyRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy ACP cache still exists: %v", err)
	}
	for _, name := range retiredRootStoreFiles {
		if _, err := os.Stat(filepath.Join(storeDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retired root file %q still exists: %v", name, err)
		}
	}
}

func TestMigrateRetiredStoreLayoutKeepsACPAgentsWhenBuiltInCodexUnavailable(t *testing.T) {
	storeDir := t.TempDir()
	legacyRoot := filepath.Join(storeDir, legacyACPAgentDirectory)
	launcher := filepath.Join(legacyRoot, "codex-acp")
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := newAppConfigStore(storeDir)
	doc := AppConfig{SchemaVersion: 2, ExternalAgents: controlagents.Configuration{
		Connections: []controlagents.Connection{{ID: "codex", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindManaged, Command: launcher}}},
	}}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	doc, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	kept, err := migrateRetiredStoreLayout(context.Background(), store, storeDir, doc, false)
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := controlagents.LookupConnection(kept.ExternalAgents, "codex")
	if connection.Launcher.Kind != controlagents.LaunchKindManaged || connection.Launcher.Command != launcher {
		t.Fatalf("unavailable migration changed launcher: %#v", connection.Launcher)
	}
	if _, err := os.Stat(legacyRoot); err != nil {
		t.Fatalf("legacy ACP cache was reclaimed without a built-in replacement: %v", err)
	}
}

func TestMigrateRetiredStoreLayoutKeepsCacheReferencedByAnotherConnection(t *testing.T) {
	storeDir := t.TempDir()
	legacyRoot := filepath.Join(storeDir, legacyACPAgentDirectory)
	codexLauncher := filepath.Join(legacyRoot, "codex-acp")
	customLauncher := filepath.Join(legacyRoot, "custom-agent")
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	store := newAppConfigStore(storeDir)
	doc := AppConfig{SchemaVersion: 2, ExternalAgents: controlagents.Configuration{
		Connections: []controlagents.Connection{
			{ID: "codex", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindManaged, Command: codexLauncher}},
			{ID: "custom", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindManaged, Command: customLauncher}},
		},
	}}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	doc, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := migrateRetiredStoreLayout(context.Background(), store, storeDir, doc, true)
	if err != nil {
		t.Fatal(err)
	}
	preparations, err := newACPPreparationStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer preparations.Close()
	if err := reclaimRetiredACPAgentStore(context.Background(), storeDir, migrated, preparations); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyRoot); err != nil {
		t.Fatalf("cache referenced by custom connection was removed: %v", err)
	}
}

func TestReclaimRetiredACPAgentStoreKeepsCacheReferencedByLivePreparation(t *testing.T) {
	storeDir := t.TempDir()
	legacyRoot := filepath.Join(storeDir, legacyACPAgentDirectory)
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	preparations, err := newACPPreparationStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer preparations.Close()
	_, err = preparations.CreatePlanned(context.Background(), controlagents.ACPPreparation{
		PrincipalID: "owner", OperationID: "prepare-1", IntentDigest: strings.Repeat("a", 64),
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: filepath.Join(legacyRoot, "custom-agent") + " --acp", ModelID: controlagents.DefaultRemoteModelID,
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reclaimRetiredACPAgentStore(context.Background(), storeDir, AppConfig{SchemaVersion: 2}, preparations); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyRoot); err != nil {
		t.Fatalf("cache referenced by live preparation was removed: %v", err)
	}
}

func TestExternalAgentConfigurationReferencesEmbeddedRootPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), legacyACPAgentDirectory)
	for name, launcher := range map[string]controlagents.Launcher{
		"argument": {
			Kind: controlagents.LaunchKindExecutable, Command: "/usr/bin/custom",
			Args: []string{"--plugin=" + filepath.Join(root, "plugin")},
		},
		"environment": {
			Kind: controlagents.LaunchKindExecutable, Command: "/usr/bin/custom",
			Env: map[string]string{"PATH": filepath.Join(root, "bin") + string(os.PathListSeparator) + "/usr/bin"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if !externalAgentConfigurationReferencesRoot(controlagents.Configuration{
				Connections: []controlagents.Connection{{ID: name, Launcher: launcher}},
			}, root) {
				t.Fatalf("launcher %#v did not retain referenced root", launcher)
			}
		})
	}
}

func TestControlStoreDatabaseRejectsSymlinkedPaths(t *testing.T) {
	storeDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, controlStoreRoot(storeDir)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := openControlStoreDatabase(storeDir); err == nil || !strings.Contains(err.Error(), "secure directory") {
		t.Fatalf("symlinked Control directory error = %v", err)
	}

	regularStore := t.TempDir()
	if err := os.MkdirAll(controlStoreRoot(regularStore), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.sqlite")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, controlStoreDatabasePath(regularStore)); err != nil {
		t.Skipf("database symlink unavailable: %v", err)
	}
	if _, _, err := openControlStoreDatabase(regularStore); err == nil || !strings.Contains(err.Error(), "secure regular file") {
		t.Fatalf("symlinked Control database error = %v", err)
	}
}
