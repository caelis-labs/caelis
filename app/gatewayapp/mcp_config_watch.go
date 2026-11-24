package gatewayapp

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"maps"
	"os"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/mcpconfig"
	"github.com/caelis-labs/caelis/control/workspacetrust"
)

const mcpConfigPollInterval = time.Second

var errMCPConfigChanged = errors.New("MCP configuration changed during reload")

// hostMCP registers live Session directories and reconciles all inherited
// configuration in one publication. Workspace paths resolve overlays; only
// the resolved launch and its authority scope partition connections.
type hostMCP struct {
	mu          sync.Mutex
	pool        *mcp.ConnectionPool
	store       *appConfigStore
	diagnostics *slog.Logger
	views       map[*mcp.Manager]hostMCPView
	workspaces  map[string]mcpWorkspaceConfig
	global      [2]mcpConfigFileStamp
	native      mcpconfig.Servers
	trust       workspacetrust.Configuration
	retry       bool
	cancel      context.CancelFunc
	done        chan struct{}
}

type hostMCPView struct {
	workspace string
	compose   func([]mcp.ServerSpec) ([]mcp.ServerSpec, error)
}

type mcpWorkspaceConfig struct {
	stamps [2]mcpConfigFileStamp
	specs  []mcp.ServerSpec
}

func newHostMCP(store *appConfigStore, initial AppConfig, diagnostics *slog.Logger) *hostMCP {
	ctx, cancel := context.WithCancel(context.Background())
	h := &hostMCP{
		pool: mcp.NewConnectionPool(context.Background()), store: store, diagnostics: diagnostics, native: initial.MCPServers, trust: initial.WorkspaceTrust,
		views: make(map[*mcp.Manager]hostMCPView), workspaces: make(map[string]mcpWorkspaceConfig),
		cancel: cancel, done: make(chan struct{}),
	}
	go h.watch(ctx)
	return h
}

func (h *hostMCP) watch(ctx context.Context) {
	defer close(h.done)
	ticker := time.NewTicker(mcpConfigPollInterval)
	defer ticker.Stop()
	var reportAt time.Time
	var reportDelay time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			h.mu.Lock()
			err := h.reloadLocked(ctx, "")
			h.mu.Unlock()
			if err == nil {
				reportAt, reportDelay = time.Time{}, 0
				continue
			}
			if h.diagnostics != nil && ctx.Err() == nil && !now.Before(reportAt) {
				reportDelay = min(max(mcpConfigPollInterval, 2*reportDelay), 15*time.Second)
				reportAt = now.Add(reportDelay)
				h.diagnostics.WarnContext(ctx, "MCP configuration reload will retry", "error", err)
			}
		}
	}
}

// configure samples inherited configuration before admitting a new view.
// An activation built from an older model or plugin snapshot cannot bring back
// a superseded inherited MCP configuration.
func (h *hostMCP) configure(ctx context.Context, manager *mcp.Manager, view hostMCPView, onFailure func(mcp.ServerFailure)) (*mcp.Manager, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var reloadErr error
	for range 3 {
		reloadErr = h.reloadLocked(ctx, view.workspace)
		if !errors.Is(reloadErr, errMCPConfigChanged) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, ok := h.workspaces[view.workspace]
	if !ok {
		return nil, errors.Join(errors.New("MCP workspace configuration is unavailable"), reloadErr)
	}
	specs, err := view.compose(config.specs)
	if err != nil {
		return nil, err
	}
	if manager == nil {
		manager, err = h.pool.NewManager(specs, onFailure)
	} else {
		err = manager.Reconcile(specs)
	}
	if err == nil {
		h.views[manager] = view
	}
	return manager, err
}

func (h *hostMCP) detach(manager *mcp.Manager) {
	h.mu.Lock()
	defer h.mu.Unlock()
	view := h.views[manager]
	delete(h.views, manager)
	for _, remaining := range h.views {
		if remaining.workspace == view.workspace {
			return
		}
	}
	delete(h.workspaces, view.workspace)
}

func (h *hostMCP) close() {
	if h == nil {
		return
	}
	h.cancel()
	<-h.done
	_ = h.pool.Close()
}

func (h *hostMCP) reloadLocked(ctx context.Context, extraWorkspace string) error {
	user, _, _ := mcpConfigFiles("")
	globalPaths := [2]string{h.store.inner.Path(), user}
	global := mcpConfigFileStamps(globalPaths)
	globalChanged := global != h.global || h.retry
	var loadErr error
	if globalChanged {
		doc, err := h.store.LoadContext(ctx)
		loadErr = err
		if err == nil {
			h.native, h.trust = doc.MCPServers, doc.WorkspaceTrust
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	paths := make(map[string]bool, len(h.workspaces)+1)
	for _, view := range h.views {
		paths[view.workspace] = true
	}
	if extraWorkspace != "" {
		paths[extraWorkspace] = true
	}
	changed := make(map[string]mcpWorkspaceConfig)
	for workspace := range paths {
		_, agents, project := mcpConfigFiles(workspace)
		if workspacetrust.Lookup(h.trust, workspace) != workspacetrust.Trusted {
			agents, project = "", ""
		}
		stamps := mcpConfigFileStamps([2]string{agents, project})
		previous, exists := h.workspaces[workspace]
		if exists && !globalChanged && previous.stamps == stamps {
			continue
		}
		specs, err := resolveConfiguredMCPServers(h.native, h.trust, workspace)
		if err != nil {
			loadErr = errors.Join(loadErr, err)
			continue
		}
		changed[workspace] = mcpWorkspaceConfig{stamps: stamps, specs: specs}
	}
	// The user overlay is shared by every workspace. Discard a scan that
	// overlapped its replacement rather than publish mixed selections.
	if len(changed) != 0 && mcpConfigFileStamps(globalPaths) != global {
		h.retry = true
		return errMCPConfigChanged
	}
	desired := make(map[*mcp.Manager][]mcp.ServerSpec)
	for manager, view := range h.views {
		config, ok := changed[view.workspace]
		if !ok {
			continue
		}
		specs, err := view.compose(config.specs)
		if err != nil {
			loadErr = errors.Join(loadErr, err)
			continue
		}
		desired[manager] = specs
	}
	if len(desired) != 0 {
		if err := h.pool.Reconcile(desired); err != nil {
			h.retry = true
			return errors.Join(loadErr, err)
		}
	}
	maps.Copy(h.workspaces, changed)
	h.global, h.retry = global, loadErr != nil
	return loadErr
}

type mcpConfigFileStamp struct {
	digest   [sha256.Size]byte
	readable bool
}

func mcpConfigFileStamps(paths [2]string) [2]mcpConfigFileStamp {
	var stamps [2]mcpConfigFileStamp
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		digest := sha256.New()
		_, err = io.Copy(digest, file)
		_ = file.Close()
		if err == nil {
			stamps[i] = mcpConfigFileStamp{digest: [sha256.Size]byte(digest.Sum(nil)), readable: true}
		}
	}
	return stamps
}
