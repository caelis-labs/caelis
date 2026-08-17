package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/plugin"
)

type PluginService = plugin.Service
type PluginInfo = plugin.Info
type MarketplaceInfo = plugin.MarketplaceInfo

// Plugins exposes the Control-owned Plugin service through this application
// host. GatewayApp supplies its product data root, revision-CAS persistence,
// candidate validation, and live MCP status snapshots.
func (s *runtimeComposition) Plugins() PluginService {
	return plugin.NewService(pluginHost{composition: s})
}

type pluginHost struct {
	composition *runtimeComposition
}

func (h pluginHost) StoreDir() string {
	if h.composition == nil {
		return ""
	}
	return h.composition.authorities.storeDir
}

func (h pluginHost) LoadPluginState(_ context.Context) (plugin.State, error) {
	if h.composition == nil || h.composition.authorities.store == nil {
		return plugin.State{}, fmt.Errorf("plugin service: stack store is unavailable")
	}
	doc, err := h.composition.authorities.store.Load()
	if err != nil {
		return plugin.State{}, err
	}
	return pluginStateFromAppConfig(doc), nil
}

func (h pluginHost) UpdatePluginState(ctx context.Context, mutation plugin.Mutation) error {
	if h.composition == nil || h.composition.authorities.store == nil {
		return fmt.Errorf("plugin service: stack store is unavailable")
	}
	if mutation.Apply == nil {
		return fmt.Errorf("plugin service: plugin state mutation is required")
	}

	oldDoc, err := h.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		return err
	}
	expected := oldDoc.ConfigurationRevision
	if revision, ok := plugin.ExpectedRevisionFromContext(ctx); ok {
		expected = revision
		if oldDoc.ConfigurationRevision != expected {
			return &configstore.ConfigurationRevisionConflict{
				Expected: expected,
				Actual:   oldDoc.ConfigurationRevision,
			}
		}
	}
	state := pluginStateFromAppConfig(oldDoc)
	if err := mutation.Apply(&state); err != nil {
		return err
	}
	state = state.Clone()
	nextDoc := oldDoc
	nextDoc.Plugins = state.Plugins
	nextDoc.PluginMarketplaces = state.Marketplaces
	if err := h.composition.validateAgentAssemblyCandidate(nextDoc); err != nil {
		return err
	}
	_, persistErr := h.composition.authorities.store.CompareAndSave(ctx, expected, nextDoc)
	if persistErr != nil && !configstore.WriteCommitted(persistErr) {
		return persistErr
	}
	var afterCommitErr error
	if mutation.AfterCommit != nil {
		afterCommitErr = mutation.AfterCommit(state.Clone())
		if afterCommitErr != nil && (persistErr == nil || configstore.WriteCommitted(persistErr)) {
			afterCommitErr = configstore.MarkWriteCommitted(afterCommitErr)
		}
	}
	return errors.Join(persistErr, afterCommitErr)
}

func (h pluginHost) MCPServersStatus(pluginID string) []mcp.MCPServerInfo {
	if h.composition == nil {
		return nil
	}
	return h.composition.MCPServersStatus(pluginID)
}

func pluginStateFromAppConfig(doc AppConfig) plugin.State {
	return (plugin.State{
		Plugins:      doc.Plugins,
		Marketplaces: doc.PluginMarketplaces,
	}).Clone()
}
