package mcp

import (
	"context"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Selection pins the set of server instances for one request while allowing
// their directories to become ready or refresh. The Manager retains ownership
// of dispatch and revocation.
type Selection struct {
	manager *Manager
	servers map[*managedServer]bool
}

// Selection captures the Manager's current desired set of server instances.
func (m *Manager) Selection() Selection {
	m.mu.Lock()
	defer m.mu.Unlock()
	selected := Selection{manager: m, servers: make(map[*managedServer]bool, len(m.servers))}
	for _, server := range m.servers {
		selected.servers[server] = true
	}
	return selected
}

// Tools lists available tools within the selected server instances.
func (s Selection) Tools() []tool.Tool {
	return s.filter(s.manager.Tools())
}

// SearchTools lists dispatchable tools within the selected server instances.
func (s Selection) SearchTools() []tool.Tool {
	return s.filter(s.manager.SearchTools())
}

func (s Selection) filter(candidates []tool.Tool) []tool.Tool {
	return slices.DeleteFunc(candidates, func(candidate tool.Tool) bool {
		remote, ok := candidate.(*MCPTool)
		return !ok || !s.servers[remote.server]
	})
}

// CheckToolBinding validates a binding within the selected server instances.
func (s Selection) CheckToolBinding(ctx context.Context, binding tool.Binding) error {
	m := s.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.toolsByName[binding.Name]; current == nil || !s.servers[current.server] || tool.BindingForDefinition(current.def) != binding {
		return tool.NewError(tool.ErrorCodeStaleRevision, "MCP binding is outside the current request selection")
	} else {
		return m.admitCatalogToolLocked(ctx, current)
	}
}
