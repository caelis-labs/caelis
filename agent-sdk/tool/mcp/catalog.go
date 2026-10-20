package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// ManagerOptions enables experimental live catalog refresh. The zero value
// preserves startup-only discovery. This does not change plugin installation.
type ManagerOptions struct{ RefreshTools bool }

// NewManagerWithOptions starts an activation-owned manager with opt-in refresh.
func NewManagerWithOptions(ctx context.Context, specs []ServerSpec, onFailure func(ServerFailure), options ManagerOptions) (*Manager, error) {
	return newManager(ctx, specs, func(ctx context.Context, spec ServerSpec) (*Client, error) {
		return startClientWithCatalog(ctx, spec, nil, options.RefreshTools)
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
			_ = m.refreshCatalog(ctx, server)
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
	found.client.catalog.invalidate()
	return m.refreshCatalog(ctx, found)
}

func (m *Manager) refreshCatalog(parent context.Context, server *managedServer) (refreshErr error) {
	server.refreshMu.Lock()
	defer server.refreshMu.Unlock()
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.closed {
			server.refreshErr = refreshErr
		}
	}()
	if err := parent.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, DefaultStartupTimeout)
	defer cancel()
	c := server.client.catalog
	c.mu.Lock()
	dirty := c.dirty
	c.mu.Unlock()
	if !dirty {
		return nil
	}
	for range 3 {
		c.mu.Lock()
		generation := c.generation
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

// CatalogRevision is an opaque activation-local invalidation token for
// background candidates. It changes on notifications, revocation, connection
// loss, and published source/schema changes, including hidden tools. It is not
// a provider cache key or a portable reconnect identity.
func (m *Manager) CatalogRevision() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, server := range m.servers {
		if server.client != nil && server.client.catalog != nil {
			server.client.catalog.mu.Lock()
			defer server.client.catalog.mu.Unlock()
		}
	}
	return m.catalogRevisionLocked()
}

// catalogRevisionLocked requires Manager.mu and every connected catalog mutex.
func (m *Manager) catalogRevisionLocked() string {
	type entry struct {
		Source                 string
		Generation             uint64
		Dirty, Revoked, Closed bool
		Failed, RefreshFailed  bool
	}
	entries := make([]entry, 0, len(m.servers))
	for _, server := range m.servers {
		item := entry{Source: server.spec.PluginID + "/" + server.spec.Name, Revoked: server.revoked, Closed: m.closed, Failed: server.err != nil, RefreshFailed: server.refreshErr != nil}
		if server.client != nil {
			item.Closed = item.Closed || server.client.closedError() != nil
			if c := server.client.catalog; c != nil {
				item.Generation = c.generation
				item.Dirty = c.dirty
				item.Revoked = c.revoked
			}
		} else {
			item.Dirty = true
		}
		entries = append(entries, item)
	}
	versions := make([]tool.Binding, 0, len(m.tools))
	for _, item := range m.tools {
		versions = append(versions, tool.BindingForDefinition(item.Definition()))
	}
	raw, _ := json.Marshal(struct {
		Entries  []entry
		Versions []tool.Binding
	}{entries, versions})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// A definition that disappears and later returns gets a new callable binding,
// even if its schema hash matches. Pending old approvals cannot revive it.
func sameCatalogTool(a, b *MCPTool) bool {
	return a.client == b.client && a.origName == b.origName && a.def.Name == b.def.Name && a.def.Metadata[tool.MetadataMCPSourceFingerprint] == b.def.Metadata[tool.MetadataMCPSourceFingerprint]
}
