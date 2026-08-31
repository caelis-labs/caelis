package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

const (
	controlStoreDirectory    = "control"
	controlStoreDatabaseFile = "control.sqlite"
	legacyACPAgentDirectory  = "acp-agents"
)

var retiredRootStoreFiles = []string{
	"config.json.assembly.lock",
	"control-host.log",
	"control-host.owner.lock",
	"control-http.token",
}

func controlStoreRoot(storeDir string) string {
	return filepath.Join(filepath.Clean(strings.TrimSpace(storeDir)), controlStoreDirectory)
}

func controlStoreDatabasePath(storeDir string) string {
	return filepath.Join(controlStoreRoot(storeDir), controlStoreDatabaseFile)
}

// migrateRetiredStoreLayout removes the retired managed-ACP installation
// authority from current configuration before reclaiming product-owned cache
// and root-level compatibility files. It runs before Runtime assembly so no
// active Runtime can retain a launcher into the retired tree.
func migrateRetiredStoreLayout(
	ctx context.Context,
	store *appConfigStore,
	storeDir string,
	doc AppConfig,
	hostedCodexAvailable bool,
) (AppConfig, error) {
	ctx = contextOrBackground(ctx)
	if store == nil {
		return AppConfig{}, errors.New("gatewayapp: app config store is required for Store migration")
	}
	storeDir, err := filepath.Abs(strings.TrimSpace(storeDir))
	if err != nil || storeDir == "" || storeDir == "." {
		return AppConfig{}, fmt.Errorf("gatewayapp: resolve Store migration root: %w", err)
	}
	storeDir = filepath.Clean(storeDir)

	if hostedCodexAvailable {
		for range 4 {
			next, changed := migrateRetiredManagedCodexLauncher(doc, storeDir)
			if !changed {
				doc = next
				break
			}
			saved, saveErr := store.CompareAndSave(ctx, doc.ConfigurationRevision, next)
			if saveErr == nil {
				doc = saved
				break
			}
			if configstore.WriteCommitted(saveErr) {
				return saved, fmt.Errorf("gatewayapp: persist retired ACP launcher migration: %w", saveErr)
			}
			if !errors.Is(saveErr, configstore.ErrConfigurationRevisionConflict) {
				return AppConfig{}, fmt.Errorf("gatewayapp: persist retired ACP launcher migration: %w", saveErr)
			}
			doc, err = store.LoadContext(ctx)
			if err != nil {
				return AppConfig{}, fmt.Errorf("gatewayapp: reload AppConfig during Store migration: %w", err)
			}
		}
		if _, changed := migrateRetiredManagedCodexLauncher(doc, storeDir); changed {
			return AppConfig{}, errors.New("gatewayapp: AppConfig kept changing during retired ACP launcher migration")
		}
	}

	for _, name := range retiredRootStoreFiles {
		if err := removeRetiredStorePath(storeDir, filepath.Join(storeDir, name)); err != nil {
			return AppConfig{}, fmt.Errorf("gatewayapp: remove retired Store file %q: %w", name, err)
		}
	}
	return doc, nil
}

func reclaimRetiredACPAgentStore(
	ctx context.Context,
	storeDir string,
	doc AppConfig,
	preparations *acpPreparationStore,
) error {
	legacyRoot := filepath.Join(storeDir, legacyACPAgentDirectory)
	if externalAgentConfigurationReferencesRoot(doc.ExternalAgents, legacyRoot) {
		return nil
	}
	referenced, err := preparations.referencesLauncherRoot(contextOrBackground(ctx), legacyRoot)
	if err != nil {
		return fmt.Errorf("gatewayapp: inspect ACP preparations before cache reclamation: %w", err)
	}
	if referenced {
		return nil
	}
	if err := removeRetiredStorePath(storeDir, legacyRoot); err != nil {
		return fmt.Errorf("gatewayapp: reclaim retired ACP agent store: %w", err)
	}
	return nil
}

func migrateRetiredManagedCodexLauncher(doc AppConfig, storeDir string) (AppConfig, bool) {
	legacyRoot := filepath.Join(storeDir, legacyACPAgentDirectory)
	changedConnections := map[string]struct{}{}
	for index, raw := range doc.ExternalAgents.Connections {
		connection := controlagents.NormalizeConnection(raw)
		if !retiredManagedCodexLauncher(connection, legacyRoot) {
			continue
		}
		connection.Launcher = controlagents.Launcher{
			Kind:      controlagents.LaunchKindHostedAdapter,
			AdapterID: "codex",
		}
		doc.ExternalAgents.Connections[index] = connection
		changedConnections[connection.ID] = struct{}{}
	}
	if len(changedConnections) == 0 {
		return doc, false
	}
	discoveries := make([]controlagents.DiscoverySnapshot, 0, len(doc.ExternalAgents.Discoveries))
	for _, discovery := range doc.ExternalAgents.Discoveries {
		if _, changed := changedConnections[controlagents.NormalizeDiscoverySnapshot(discovery).ConnectionID]; changed {
			continue
		}
		discoveries = append(discoveries, discovery)
	}
	doc.ExternalAgents.Discoveries = discoveries
	return configstore.Normalize(doc), true
}

func retiredManagedCodexLauncher(connection controlagents.Connection, legacyRoot string) bool {
	launcher := controlagents.NormalizeLauncher(connection.Launcher)
	if launcher.Kind != controlagents.LaunchKindManaged && launcher.Kind != controlagents.LaunchKindPackageExec {
		return false
	}
	if !pathWithinRoot(launcher.Command, legacyRoot) {
		return false
	}
	commandName := strings.ToLower(filepath.Base(launcher.Command))
	commandName = strings.TrimSuffix(commandName, filepath.Ext(commandName))
	return connection.ID == "codex" || commandName == "codex-acp"
}

func externalAgentConfigurationReferencesRoot(configuration controlagents.Configuration, root string) bool {
	for _, connection := range configuration.Connections {
		launcher := controlagents.NormalizeLauncher(connection.Launcher)
		if textReferencesRoot(launcher.Command, root) || textReferencesRoot(launcher.WorkDir, root) {
			return true
		}
		for _, value := range launcher.Args {
			if textReferencesRoot(value, root) {
				return true
			}
		}
		for _, value := range launcher.Env {
			if textReferencesRoot(value, root) {
				return true
			}
		}
	}
	return false
}

func textReferencesRoot(value string, root string) bool {
	if pathWithinRoot(value, root) {
		return true
	}
	value = strings.TrimSpace(value)
	root, err := filepath.Abs(strings.TrimSpace(root))
	if value == "" || err != nil {
		return false
	}
	return strings.Contains(value, filepath.Clean(root))
}

func pathWithinRoot(candidate string, root string) bool {
	candidate = strings.TrimSpace(candidate)
	root = strings.TrimSpace(root)
	if candidate == "" || root == "" {
		return false
	}
	absoluteCandidate, candidateErr := filepath.Abs(candidate)
	absoluteRoot, rootErr := filepath.Abs(root)
	if candidateErr != nil || rootErr != nil {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(absoluteRoot), filepath.Clean(absoluteCandidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func removeRetiredStorePath(storeDir string, path string) error {
	storeDir = filepath.Clean(storeDir)
	path = filepath.Clean(path)
	relative, err := filepath.Rel(storeDir, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("refusing to remove a path outside the Store")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return os.RemoveAll(path)
}
