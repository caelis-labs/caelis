package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// ManagerOptions is retained for source compatibility. Live catalog refresh is
// built in and always enabled, so the values here no longer change behaviour.
type ManagerOptions struct {
	// RefreshTools is deprecated and ignored: refresh is unconditional.
	RefreshTools bool
}

// NewManagerWithOptions starts an activation-owned manager with live catalog
// refresh always enabled.
func NewManagerWithOptions(ctx context.Context, specs []ServerSpec, onFailure func(ServerFailure), _ ManagerOptions) (*Manager, error) {
	return newManager(ctx, specs, func(ctx context.Context, spec ServerSpec) (*Client, error) {
		return startClientWithCatalog(ctx, spec, nil, true)
	}, onFailure)
}

type clientCatalog struct {
	mu         sync.Mutex
	generation uint64
	dirty      bool
	revoked    bool
	changed    chan struct{}
}

func newClientCatalog() *clientCatalog {
	return &clientCatalog{dirty: true, changed: make(chan struct{}, 1)}
}
func (c *clientCatalog) invalidate() {
	c.mu.Lock()
	c.generation++
	c.dirty = true
	c.mu.Unlock()
	select {
	case c.changed <- struct{}{}:
	default:
	}
}
func (m *Manager) admitCatalogTool(ctx context.Context, remote *MCPTool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := remote.client.catalog
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !m.closed && (c == nil || (!c.revoked && !c.dirty)) {
		for _, candidate := range m.tools {
			current, ok := candidate.(*MCPTool)
			if ok && current.bindingID == remote.bindingID && sameCatalogTool(current, remote) {
				return nil
			}
		}
	}
	return errors.New("MCP tool catalog changed or access was revoked; discover again after refresh")
}

func (m *Manager) watchCatalog(ctx context.Context, server *managedServer) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-server.client.closed:
			return
		case <-server.client.catalog.changed:
			_ = m.refreshCatalog(ctx, server, false)
		}
	}
}

// RefreshTools retries a failed catalog read explicitly. Failure retains the
// last whole directory but leaves dispatch blocked until validation succeeds.
func (m *Manager) RefreshTools(ctx context.Context, pluginID, serverName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	var found *managedServer
	for _, s := range m.servers {
		if s.spec.PluginID == pluginID && s.spec.Name == serverName && s.client != nil && s.client.catalog != nil {
			found = s
			break
		}
	}
	closed := m.closed
	m.mu.Unlock()
	if found == nil || closed {
		return errors.New("MCP refresh is unavailable")
	}
	// Ownership is claimed inside refreshCatalog. Invalidating before it would
	// wake the watcher on behalf of a waiter that may already be cancelled.
	return m.refreshCatalog(ctx, found, true)
}

// refreshCatalog validates one server directory. Ownership is a capacity-one
// gate so a waiter can abandon the attempt without starting tools/list, and the
// work timeout only starts once ownership is held. force marks the directory
// dirty after acquiring ownership: a cancelled manual request has no effect.
func (m *Manager) refreshCatalog(parent context.Context, server *managedServer, force bool) (refreshErr error) {
	if err := parent.Err(); err != nil {
		return err
	}
	// Fixtures may construct managedServer directly; initialize lazily rather
	// than requiring every constructor to know about the gate.
	m.mu.Lock()
	if server.refreshGate == nil {
		server.refreshGate = make(chan struct{}, 1)
	}
	gate := server.refreshGate
	m.mu.Unlock()
	select {
	case gate <- struct{}{}:
	case <-parent.Done():
		return parent.Err()
	}
	defer func() { <-gate }()
	// Both cases can be ready at once; a cancelled waiter must not proceed.
	if err := parent.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, DefaultStartupTimeout)
	defer cancel()
	c := server.client.catalog
	m.mu.Lock()
	c.mu.Lock()
	if force {
		c.generation++
		c.dirty = true
	}
	dirty, generation := c.dirty, c.generation
	c.mu.Unlock()
	if !dirty {
		m.mu.Unlock()
		return nil
	}
	server.refreshing = true
	server.refreshErr = nil
	m.mu.Unlock()
	defer func() { m.finishRefresh(server, generation, refreshErr) }()
	for range 3 {
		c.mu.Lock()
		generation = c.generation
		revoked := c.revoked
		c.mu.Unlock()
		if revoked {
			return errors.New("MCP server access revoked")
		}
		listed, err := server.client.ListTools(ctx)
		if err != nil {
			return err
		}
		m.mu.Lock()
		c.mu.Lock()
		if m.closed || c.revoked || ctx.Err() != nil {
			c.mu.Unlock()
			m.mu.Unlock()
			return errors.New("MCP refresh cancelled")
		}
		if c.generation != generation {
			c.mu.Unlock()
			m.mu.Unlock()
			continue
		}
		server.listed = listed
		server.catalogReady = true
		server.err = nil
		m.rebuildToolsLocked()
		c.dirty = false
		c.mu.Unlock()
		m.mu.Unlock()
		return nil
	}
	return errors.New("MCP catalog changed during pagination; previous directory retained")
}

// finishRefresh records one finished attempt. generation identifies the
// directory revision the attempt actually covered, so an older failure cannot
// be read as a settled failure of a newer revision.
func (m *Manager) finishRefresh(server *managedServer, generation uint64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Clear the in-flight marker even when the Manager is closing: a stuck
	// refreshing flag would present a directory that never settles.
	server.refreshing = false
	if m.closed {
		return
	}
	server.refreshErr = err
	server.refreshErrGeneration = generation
}

// settledRefreshFailed requires Manager.mu. Only a failure that covered the
// current generation may be presented as a finished, retryable attempt.
func settledRefreshFailed(server *managedServer, generation uint64) bool {
	return server != nil && server.refreshErr != nil && server.refreshErrGeneration == generation
}

// RevokeServer closes dispatch immediately, including old pinned callables and
// calls waiting for approval. It retains the connection and existing results;
// the embedding owner still owns server/plugin shutdown and configuration.
func (m *Manager) RevokeServer(pluginID, serverName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, server := range m.servers {
		if server.spec.PluginID != pluginID || server.spec.Name != serverName {
			continue
		}
		if server.client == nil {
			return errors.New("MCP server is unavailable")
		}
		c := server.client.catalog
		if c != nil {
			c.mu.Lock()
			c.revoked = true
			c.generation++
			c.mu.Unlock()
		}
		server.revoked = true
		m.rebuildToolsLocked()
		return nil
	}
	return fmt.Errorf("MCP server %s/%s not found", pluginID, serverName)
}

// A definition that disappears and later returns gets a new callable binding,
// even if its schema hash matches. Pending old approvals cannot revive it.
func sameCatalogTool(a, b *MCPTool) bool {
	return a.client == b.client && a.origName == b.origName && a.def.Name == b.def.Name && a.def.Metadata[tool.MetadataMCPSourceFingerprint] == b.def.Metadata[tool.MetadataMCPSourceFingerprint]
}
