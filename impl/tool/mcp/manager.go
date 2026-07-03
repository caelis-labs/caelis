package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/ports/plugin"
	"github.com/caelis-labs/caelis/ports/tool"
)

type MCPServerInfo struct {
	Name    string
	Status  string
	Tools   []string
	Warning string
}

type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client // Key: pluginID + "/" + serverName
	tools   []tool.Tool
}

func formatToolName(pluginID, serverName, toolName string) string {
	raw := fmt.Sprintf("mcp__%s__%s__%s", pluginID, serverName, toolName)
	name := sanitizeToolName(raw)
	if len(name) <= 64 {
		return name
	}
	return shortenToolName(name, raw)
}

func NewManager(ctx context.Context, specs []plugin.MCPServerSpec) (*Manager, error) {
	mgr := &Manager{
		clients: make(map[string]*Client),
	}
	usedToolNames := map[string]string{}

	for _, spec := range specs {
		client, err := StartClient(ctx, spec)
		if err != nil {
			_ = mgr.Close()
			return nil, fmt.Errorf("mcp manager: failed to start server %s/%s: %w", spec.PluginID, spec.Name, err)
		}

		key := spec.PluginID + "/" + spec.Name
		mgr.clients[key] = client

		listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		toolInfos, err := client.ListTools(listCtx)
		cancel()
		if err != nil {
			_ = mgr.Close()
			return nil, fmt.Errorf("mcp manager: failed to list tools for %s/%s: %w", spec.PluginID, spec.Name, err)
		}

		for _, info := range toolInfos {
			if info == nil || strings.TrimSpace(info.Name) == "" {
				continue
			}
			identity := spec.PluginID + "\x00" + spec.Name + "\x00" + info.Name
			namespacedName := uniqueToolName(formatToolName(spec.PluginID, spec.Name, info.Name), identity, usedToolNames)
			t := &MCPTool{
				client:     client,
				pluginID:   spec.PluginID,
				serverName: spec.Name,
				origName:   info.Name,
				def: tool.Definition{
					Name:        namespacedName,
					Description: info.Description,
					InputSchema: inputSchemaMap(info.InputSchema),
					Metadata: map[string]any{
						tool.MetadataToolKind:  tool.MetadataToolKindMCP,
						tool.MetadataPluginID:  spec.PluginID,
						tool.MetadataMCPServer: spec.Name,
						tool.MetadataMCPTool:   info.Name,
					},
				},
			}
			mgr.tools = append(mgr.tools, t)
		}
	}
	sort.SliceStable(mgr.tools, func(i, j int) bool {
		return mgr.tools[i].Definition().Name < mgr.tools[j].Definition().Name
	})

	return mgr, nil
}

func sanitizeToolName(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		var out rune
		switch {
		case r >= 'a' && r <= 'z':
			out = r
		case r >= 'A' && r <= 'Z':
			out = r + ('a' - 'A')
		case r >= '0' && r <= '9':
			out = r
		case r == '_':
			out = '_'
		default:
			out = '_'
		}
		b.WriteRune(out)
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		return "mcp_tool"
	}
	if first := name[0]; (first < 'a' || first > 'z') && first != '_' {
		name = "mcp_" + name
	}
	return name
}

func uniqueToolName(name string, identity string, used map[string]string) string {
	if used == nil {
		return name
	}
	if existing, ok := used[name]; !ok || existing == identity {
		used[name] = identity
		return name
	}
	name = shortenToolName(name, identity)
	for i := 0; ; i++ {
		candidate := name
		if i > 0 {
			candidate = shortenToolName(fmt.Sprintf("%s_%d", name, i), identity)
		}
		if existing, ok := used[candidate]; !ok || existing == identity {
			used[candidate] = identity
			return candidate
		}
	}
}

func shortenToolName(name string, identity string) string {
	const maxToolNameLen = 64
	sum := sha256.Sum256([]byte(identity))
	suffix := fmt.Sprintf("%x", sum[:])[:12]
	budget := maxToolNameLen - len(suffix) - 2
	if budget < len("mcp") {
		return "mcp_" + suffix
	}
	prefix := name
	if len(prefix) > budget {
		prefix = prefix[:budget]
	}
	prefix = strings.Trim(prefix, "_")
	if prefix == "" {
		prefix = "mcp"
	}
	return prefix + "__" + suffix
}

func inputSchemaMap(schema any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object"}
	}
	if typed, ok := schema.(map[string]any); ok {
		return typed
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{"type": "object"}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || len(out) == 0 {
		return map[string]any{"type": "object"}
	}
	return out
}

func (m *Manager) Tools() []tool.Tool {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]tool.Tool(nil), m.tools...)
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, client := range m.clients {
		_ = client.Close()
	}
	m.clients = make(map[string]*Client)
	m.tools = nil
	return nil
}

func (m *Manager) GetServerInfos(pluginID string) []MCPServerInfo {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var infos []MCPServerInfo
	for key, client := range m.clients {
		parts := strings.Split(key, "/")
		if len(parts) != 2 || parts[0] != pluginID {
			continue
		}
		serverName := parts[1]

		status := "running"
		var warning string
		select {
		case <-client.closed:
			status = "failed"
			if client.closeErr != nil {
				warning = client.closeErr.Error()
			}
		default:
		}

		var tools []string
		for _, t := range m.tools {
			if mcpTool, ok := t.(*MCPTool); ok && mcpTool.client == client {
				tools = append(tools, mcpTool.origName)
			}
		}
		sort.Strings(tools)

		infos = append(infos, MCPServerInfo{
			Name:    serverName,
			Status:  status,
			Tools:   tools,
			Warning: warning,
		})
	}
	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].Name < infos[j].Name
	})
	return infos
}
