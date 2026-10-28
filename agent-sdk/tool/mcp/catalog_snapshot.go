package mcp

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"sync"
)

// ValidateCatalog checks a captured CatalogRevision and holds the complete
// directory identity until release. It is an opt-in composition seam for a
// short checkpoint commit, never for generation, approval or remote execution.
// A settled failed/disconnected server is a valid captured directory state; its
// tools remain inadmissible. It does not admit a model/tool request. Contention
// fails closed without waiting.
//
// The guard serializes notifications, refresh publication, local connection-loss
// publication, revocation and dispatch admission. Updates linearize before or
// after that critical section, never at a later compact. The caller must release
// on every path and must not reenter this Manager or its clients while holding it.
// Other configuration/permission owners need their own guards, acquired in a
// consistent order. The embedding owns mapping this error to snapshot staleness.
func (m *Manager) ValidateCatalog(ctx context.Context, revision string) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m == nil || !m.mu.TryLock() {
		return nil, tool.ErrCatalogSnapshotStale
	}
	var locked []func()
	release = sync.OnceFunc(func() {
		for i := len(locked) - 1; i >= 0; i-- {
			locked[i]()
		}
		m.mu.Unlock()
	})
	defer func() {
		if err != nil {
			release()
			release = nil
		}
	}()
	if m.closed || revision == "" {
		return release, tool.ErrCatalogSnapshotStale
	}
	for _, server := range m.servers {
		if server.client == nil {
			if server.err == nil {
				return release, tool.ErrCatalogSnapshotStale
			}
			continue // A settled startup failure is part of the captured directory.
		}
		c := server.client.catalog
		if c == nil {
			if !server.client.identityMu.TryLock() {
				return release, tool.ErrCatalogSnapshotStale
			}
			locked = append(locked, server.client.identityMu.Unlock)
			continue
		}
		if !c.mu.TryLock() {
			return release, tool.ErrCatalogSnapshotStale
		}
		locked = append(locked, c.mu.Unlock)
		if server.refreshing {
			return release, tool.ErrCatalogSnapshotStale
		}
		if c.dirty && !settledRefreshFailed(server, c.generation) && server.client.closedError() == nil {
			return release, tool.ErrCatalogSnapshotStale
		}
	}
	if err := ctx.Err(); err != nil {
		return release, err
	}
	if m.catalogRevisionLocked() != revision {
		return release, tool.ErrCatalogSnapshotStale
	}
	return release, nil
}
