package mcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type MCPServerInfo struct {
	Name    string
	Status  string
	Tools   []string
	Warning string
}

type Manager struct {
	mu       sync.Mutex
	clients  map[string]*Client // Key: pluginID + "/" + serverName
	tools    []tool.Tool
	warnings map[string][]string
}

func formatToolName(serverName, toolName string) string {
	raw := fmt.Sprintf("%s__%s", serverName, toolName)
	name := sanitizeToolName(raw)
	if len(name) <= 64 {
		return name
	}
	return shortenToolName(name, raw)
}

func legacyToolName(pluginID, serverName, toolName string) string {
	raw := fmt.Sprintf("mcp__%s__%s__%s", pluginID, serverName, toolName)
	name := sanitizeToolName(raw)
	if len(name) <= 64 {
		return name
	}
	return shortenToolName(name, raw)
}

func NewManager(ctx context.Context, specs []ServerSpec) (*Manager, error) {
	return newManager(ctx, specs, StartClient)
}

type clientStarter func(context.Context, ServerSpec) (*Client, error)

func newManager(ctx context.Context, specs []ServerSpec, start clientStarter) (*Manager, error) {
	mgr := &Manager{
		clients:  make(map[string]*Client),
		warnings: make(map[string][]string),
	}
	// Compact names intentionally omit source identity. Keep every server
	// running, but expose only the first accepted definition for a projected
	// name; later servers can still contribute names not already claimed.
	toolsByProjectedName := map[string]*MCPTool{}
	seenServers := make(map[string]struct{}, len(specs))

	for _, spec := range specs {
		if err := validateMCPIdentity("plugin id", spec.PluginID, maxMCPPluginIDRunes); err != nil {
			_ = mgr.Close()
			return nil, fmt.Errorf("mcp manager: %w", err)
		}
		for _, sourceID := range spec.ReplaySourceIDs {
			if err := validateMCPIdentity("replay source id", sourceID, maxMCPPluginIDRunes); err != nil {
				_ = mgr.Close()
				return nil, fmt.Errorf("mcp manager: %w", err)
			}
		}
		if err := validateMCPIdentity("server name", spec.Name, maxMCPServerNameRunes); err != nil {
			_ = mgr.Close()
			return nil, fmt.Errorf("mcp manager: %w", err)
		}
		key := spec.PluginID + "/" + spec.Name
		if _, exists := seenServers[key]; exists {
			_ = mgr.Close()
			return nil, fmt.Errorf("mcp manager: duplicate server %s/%s", spec.PluginID, spec.Name)
		}
		seenServers[key] = struct{}{}
	}

	for _, spec := range specs {
		key := spec.PluginID + "/" + spec.Name
		client, err := start(ctx, spec)
		if err != nil {
			_ = mgr.Close()
			return nil, fmt.Errorf("mcp manager: failed to start server %s/%s: %w", spec.PluginID, spec.Name, err)
		}
		mgr.clients[key] = client

		listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		toolInfos, err := client.ListTools(listCtx)
		cancel()
		if err != nil {
			_ = mgr.Close()
			return nil, fmt.Errorf("mcp manager: failed to list tools for %s/%s: %w", spec.PluginID, spec.Name, err)
		}

		sort.SliceStable(toolInfos, func(i, j int) bool {
			if toolInfos[i] == nil {
				return false
			}
			if toolInfos[j] == nil {
				return true
			}
			return toolInfos[i].Name < toolInfos[j].Name
		})
		acceptedForServer := 0
		for _, info := range toolInfos {
			if info == nil || strings.TrimSpace(info.Name) == "" {
				continue
			}
			toolLabel := mcpWarningToolName(info.Name)
			if err := validateMCPIdentity("remote tool name", info.Name, maxMCPRemoteToolNameRunes); err != nil {
				mgr.addWarning(key, fmt.Sprintf("tool %s quarantined: %v", toolLabel, err))
				continue
			}
			if acceptedForServer >= maxMCPToolsPerServer || len(mgr.tools) >= maxMCPToolsPerManager {
				mgr.addWarning(key, fmt.Sprintf("tool %s quarantined: MCP tool count limit reached", toolLabel))
				continue
			}
			name := formatToolName(spec.Name, info.Name)
			legacyNames := legacyToolNames(spec, info.Name)
			if winner := toolsByProjectedName[name]; winner != nil {
				winner.addReplayAliases(legacyNames)
				continue
			}
			def, warning, err := normalizeListedToolDefinition(tool.Definition{
				Name:        name,
				Description: info.Description,
				Metadata: map[string]any{
					tool.MetadataToolKind:      tool.MetadataToolKindMCP,
					tool.MetadataPluginID:      spec.PluginID,
					tool.MetadataMCPServer:     spec.Name,
					tool.MetadataMCPTool:       info.Name,
					tool.MetadataReplayAliases: legacyNames,
				},
			}, info.InputSchema)
			if warning != "" {
				mgr.addWarning(key, fmt.Sprintf("tool %s: %s", toolLabel, warning))
			}
			if err != nil {
				mgr.addWarning(key, fmt.Sprintf("tool %s quarantined: %v", toolLabel, err))
				continue
			}
			t := &MCPTool{
				client:     client,
				pluginID:   spec.PluginID,
				serverName: spec.Name,
				origName:   info.Name,
				def:        def,
			}
			toolsByProjectedName[name] = t
			mgr.tools = append(mgr.tools, t)
			acceptedForServer++
		}
	}
	sort.SliceStable(mgr.tools, func(i, j int) bool {
		return mgr.tools[i].Definition().Name < mgr.tools[j].Definition().Name
	})

	return mgr, nil
}

func legacyToolNames(spec ServerSpec, toolName string) []string {
	sourceIDs := make([]string, 0, 1+len(spec.ReplaySourceIDs))
	sourceIDs = append(sourceIDs, spec.PluginID)
	sourceIDs = append(sourceIDs, spec.ReplaySourceIDs...)
	seen := make(map[string]struct{}, len(sourceIDs))
	names := make([]string, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		name := legacyToolName(sourceID, spec.Name, toolName)
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func mcpWarningToolName(name string) string {
	const maxWarningNameRunes = 80
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxWarningNameRunes {
		name = truncateRunes(name, maxWarningNameRunes) + "..."
	}
	return fmt.Sprintf("%q", name)
}

func (m *Manager) addWarning(key string, warning string) {
	if m == nil || strings.TrimSpace(warning) == "" {
		return
	}
	warnings := m.warnings[key]
	if len(warnings) < maxMCPWarningsPerServer {
		m.warnings[key] = append(warnings, warning)
		return
	}
	if len(warnings) == maxMCPWarningsPerServer {
		m.warnings[key] = append(warnings, "additional MCP ingress warnings omitted")
	}
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
		warnings := append([]string(nil), m.warnings[key]...)
		select {
		case <-client.closed:
			status = "failed"
			if client.closeErr != nil {
				warnings = append(warnings, client.closeErr.Error())
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
			Warning: strings.Join(warnings, "; "),
		})
	}
	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].Name < infos[j].Name
	})
	return infos
}
