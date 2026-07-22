package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/plugin"
)

type PluginService = plugin.Service
type PluginInfo = plugin.Info
type MarketplaceInfo = plugin.MarketplaceInfo

// Plugins exposes the Control-owned Plugin service through this application
// host. GatewayApp supplies its product data root, persistence and mutation
// fencing, Runtime replacement and rollback, and live MCP status snapshots.
func (s *Stack) Plugins() PluginService {
	return plugin.NewService(pluginHost{stack: s})
}

type pluginHost struct {
	stack *Stack
}

func (h pluginHost) StoreDir() string {
	if h.stack == nil {
		return ""
	}
	return h.stack.storeDir
}

func (h pluginHost) LoadPluginState(_ context.Context) (plugin.State, error) {
	if h.stack == nil || h.stack.store == nil {
		return plugin.State{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	doc, err := h.stack.store.Load()
	if err != nil {
		return plugin.State{}, err
	}
	return pluginStateFromAppConfig(doc), nil
}

func (h pluginHost) UpdatePluginState(_ context.Context, mutation plugin.Mutation) error {
	if h.stack == nil || h.stack.store == nil {
		return fmt.Errorf("plugin service: stack store is unavailable")
	}
	if mutation.Apply == nil {
		return fmt.Errorf("plugin service: plugin state mutation is required")
	}

	h.stack.reconfigureMu.Lock()
	defer h.stack.reconfigureMu.Unlock()
	if mutation.Reconfigure {
		h.stack.assemblyMutationMu.Lock()
		defer h.stack.assemblyMutationMu.Unlock()
	}
	if err := h.stack.rejectReconfigureWhileActive(mutation.GuardAction); err != nil {
		return err
	}

	oldDoc, err := h.stack.store.Load()
	if err != nil {
		return err
	}
	state := pluginStateFromAppConfig(oldDoc)
	if err := mutation.Apply(&state); err != nil {
		return err
	}
	state = state.Clone()
	nextDoc := oldDoc
	nextDoc.Plugins = state.Plugins
	nextDoc.PluginMarketplaces = state.Marketplaces
	if err := h.stack.store.Save(nextDoc); err != nil {
		return err
	}
	if mutation.Reconfigure {
		if err := h.stack.rebuildGateway(); err != nil {
			return h.rollbackPluginMutation(mutation.FailureAction, err, oldDoc)
		}
	}
	if mutation.AfterCommit != nil {
		return mutation.AfterCommit(state.Clone())
	}
	return nil
}

func (h pluginHost) MCPServersStatus(pluginID string) []mcp.MCPServerInfo {
	if h.stack == nil {
		return nil
	}
	return h.stack.MCPServersStatus(pluginID)
}

func (h pluginHost) rollbackPluginMutation(action string, mutationErr error, oldDoc AppConfig) error {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "reconfigure gateway plugins"
	}
	rollbackErr := h.stack.store.Save(oldDoc)
	if rollbackErr != nil {
		return fmt.Errorf("plugin service: failed to %s (rollback config save failed): %w, rollback error: %w", action, mutationErr, rollbackErr)
	}
	if rebuildErr := h.stack.rebuildGateway(); rebuildErr != nil {
		return fmt.Errorf("plugin service: failed to %s (rollback rebuild failed): %w, rollback error: %w", action, mutationErr, rebuildErr)
	}
	return fmt.Errorf("plugin service: failed to %s (rollback successful): %w", action, mutationErr)
}

func pluginStateFromAppConfig(doc AppConfig) plugin.State {
	return (plugin.State{
		Plugins:      doc.Plugins,
		Marketplaces: doc.PluginMarketplaces,
	}).Clone()
}
