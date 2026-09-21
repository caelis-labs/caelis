package gatewayapp

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/agentregistry"
	"github.com/caelis-labs/caelis/control/agents"
)

// InstalledCommand resolves the user's configured runtime before PATH and the
// default installation directory. Reopening setup preserves a chosen directory.
func (s AgentService) InstalledCommand(ctx context.Context, adapterID string) (string, error) {
	var store *appConfigStore
	if s.composition != nil {
		store = s.composition.authorities.store
	}
	return findInstalledACPCommand(ctx, store, adapterID)
}

func findInstalledACPCommand(ctx context.Context, store *appConfigStore, adapterID string) (string, error) {
	entry, ok := agentregistry.LookupConnectableAgent(adapterID)
	if ok && entry.Installation != nil && store != nil {
		doc, err := store.LoadContext(ctx)
		if err != nil {
			return "", err
		}
		if connection, ok := agents.LookupConnection(doc.ExternalAgents, adapterID); ok {
			launcher := connection.Launcher
			base := filepath.Base(launcher.Command)
			matchesCommand := base == entry.Installation.Command
			if runtime.GOOS == "windows" {
				matchesCommand = strings.EqualFold(base, entry.Installation.Command)
			}
			if launcher.Kind == agents.LaunchKindExecutable && filepath.IsAbs(launcher.Command) && matchesCommand {
				if _, err := exec.LookPath(launcher.Command); err == nil {
					return launcher.Command, nil
				}
			}
		}
	}
	return agentregistry.FindInstalledCommand(adapterID), nil
}
