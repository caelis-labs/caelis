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

// PluginReadService is the read-only Plugin projection available to
// Host-private AppServer assembly. Plugin mutations remain on principal-bound
// commands and are never exposed by a Session Runtime lease.
type PluginReadService struct {
	service plugin.Service
}

// Plugins exposes the Control-owned mutable Plugin service only through the
// process Host. Detached Runtime compositions return a zero service so their
// hook-free configuration readers cannot become write authorities.
func (s *runtimeComposition) Plugins() PluginService {
	if s == nil || s.process == nil || s.process.config == nil {
		return PluginService{}
	}
	return plugin.NewService(pluginHost{composition: s})
}

// pluginReadBackend supplies the Host's live Plugin view or a detached
// Runtime's activation-pinned view through a Host that rejects every mutation.
func (s *runtimeComposition) pluginReadBackend() PluginService {
	if s == nil {
		return PluginService{}
	}
	return plugin.NewService(pluginReadHost{composition: s})
}

// pluginCacheLifecycleBackend reads canonical Plugin configuration while
// reclaiming unpinned managed content. It deliberately does not use a detached
// Runtime's activation snapshot, and it still rejects configuration writes.
func (s *runtimeComposition) pluginCacheLifecycleBackend() PluginService {
	if s == nil {
		return PluginService{}
	}
	return plugin.NewService(pluginCacheLifecycleHost{composition: s})
}

func (s *runtimeComposition) pluginReads() PluginReadService {
	if s == nil {
		return PluginReadService{}
	}
	return PluginReadService{service: s.pluginReadBackend()}
}

// ControlPluginReads returns the focused read-only Plugin projection used by
// AppServer composition.
func (s *Stack) ControlPluginReads() PluginReadService {
	if s == nil {
		return PluginReadService{}
	}
	return s.composition.pluginReads()
}

// List returns the current Plugin catalog.
func (s PluginReadService) List(ctx context.Context) ([]PluginInfo, error) {
	return s.service.List(ctx)
}

// ListMarketplaces returns the current Plugin marketplace catalog.
func (s PluginReadService) ListMarketplaces(ctx context.Context) ([]MarketplaceInfo, error) {
	return s.service.ListMarketplaces(ctx)
}

// Inspect returns one current Plugin snapshot.
func (s PluginReadService) Inspect(ctx context.Context, id string) (PluginInfo, error) {
	return s.service.Inspect(ctx, id)
}

type pluginHost struct {
	composition *runtimeComposition
}

type pluginReadHost struct {
	composition *runtimeComposition
}

type pluginCacheLifecycleHost struct {
	composition *runtimeComposition
}

func (h pluginReadHost) StoreDir() string {
	return pluginHost(h).StoreDir()
}

func (h pluginReadHost) LoadPluginState(ctx context.Context) (plugin.State, error) {
	if h.composition != nil && h.composition.activation != nil && h.composition.activation.appConfig != nil {
		return pluginStateFromAppConfig(*h.composition.activation.appConfig), nil
	}
	return pluginHost(h).LoadPluginState(ctx)
}

func (pluginReadHost) UpdatePluginState(context.Context, plugin.Mutation) error {
	return plugin.ErrHostUnavailable
}

func (h pluginReadHost) MCPServersStatus(pluginID string) []mcp.MCPServerInfo {
	return pluginHost(h).MCPServersStatus(pluginID)
}

func (h pluginCacheLifecycleHost) StoreDir() string {
	return pluginHost(h).StoreDir()
}

func (h pluginCacheLifecycleHost) LoadPluginState(ctx context.Context) (plugin.State, error) {
	return pluginHost(h).LoadPluginState(ctx)
}

func (pluginCacheLifecycleHost) UpdatePluginState(context.Context, plugin.Mutation) error {
	return plugin.ErrHostUnavailable
}

func (h pluginCacheLifecycleHost) MCPServersStatus(pluginID string) []mcp.MCPServerInfo {
	return pluginHost(h).MCPServersStatus(pluginID)
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
