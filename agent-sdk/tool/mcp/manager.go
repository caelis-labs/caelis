package mcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPServerInfo struct {
	Name    string
	Status  string
	Tools   []string
	Warning string
}

// ServerFailure describes one failed initialization attempt. Err is private
// diagnostic data; presentation adapters must supply bounded public wording.
type ServerFailure struct {
	PluginID string
	Name     string
	Err      error
}

type managedServer struct {
	spec   ServerSpec
	client *Client
	listed []*mcpsdk.Tool
	err    error
}

// Manager owns background MCP initialization and immutable ready-tool snapshots.
type Manager struct {
	mu          sync.Mutex
	servers     []*managedServer
	tools       []tool.Tool
	warnings    map[string][]string
	cancel      context.CancelFunc
	initialized chan struct{}
	closed      bool
	closeOnce   sync.Once
	onFailure   func(ServerFailure)
}

// Initialized closes after every server has either initialized or failed.
// Tool consumers do not need to wait: Tools returns only ready definitions.
func (m *Manager) Initialized() <-chan struct{} { return m.initialized }

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

// NewManager starts each server independently in the background. Connection or
// listing failures affect only that server and invoke onFailure once, if set.
// ctx owns initialization; Close cancels and drains all initialization work.
func NewManager(ctx context.Context, specs []ServerSpec, onFailure func(ServerFailure)) (*Manager, error) {
	return newManager(ctx, specs, StartClient, onFailure)
}

type clientStarter func(context.Context, ServerSpec) (*Client, error)

func newManager(ctx context.Context, specs []ServerSpec, start clientStarter, onFailure func(ServerFailure)) (*Manager, error) {
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if err := validateMCPIdentity("plugin id", spec.PluginID, maxMCPPluginIDRunes); err != nil {
			return nil, err
		}
		if err := validateMCPIdentity("server name", spec.Name, maxMCPServerNameRunes); err != nil {
			return nil, err
		}
		for _, id := range spec.ReplaySourceIDs {
			if err := validateMCPIdentity("replay source id", id, maxMCPPluginIDRunes); err != nil {
				return nil, err
			}
		}
		key := spec.PluginID + "/" + spec.Name
		if seen[key] {
			return nil, fmt.Errorf("mcp manager: duplicate server %s", key)
		}
		seen[key] = true
	}
	ctx, cancel := context.WithCancel(ctx)
	mgr := &Manager{warnings: make(map[string][]string), cancel: cancel, initialized: make(chan struct{}), onFailure: onFailure}
	for _, spec := range specs {
		spec.Args = append([]string(nil), spec.Args...)
		spec.ReplaySourceIDs = append([]string(nil), spec.ReplaySourceIDs...)
		spec.Env = maps.Clone(spec.Env)
		spec.Headers = maps.Clone(spec.Headers)
		mgr.servers = append(mgr.servers, &managedServer{spec: spec})
	}
	var workers sync.WaitGroup
	for _, server := range mgr.servers {
		workers.Go(func() { mgr.initialize(ctx, server, start) })
	}
	go func() { workers.Wait(); close(mgr.initialized) }()
	return mgr, nil
}

func (m *Manager) initialize(parent context.Context, server *managedServer, start clientStarter) {
	ctx, cancel := context.WithTimeout(parent, DefaultStartupTimeout)
	defer cancel()
	client, err := start(ctx, server.spec)
	var listed []*mcpsdk.Tool
	if err == nil {
		listed, err = client.ListTools(ctx)
	}
	if err == nil {
		err = ctx.Err()
	}
	m.mu.Lock()
	if m.closed || parent.Err() != nil {
		m.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		return
	}
	server.err = err
	if err == nil {
		server.client, server.listed = client, listed
	}
	m.rebuildToolsLocked()
	m.mu.Unlock()
	if err != nil {
		if client != nil {
			_ = client.Close()
		}
		if m.onFailure != nil {
			m.onFailure(ServerFailure{PluginID: server.spec.PluginID, Name: server.spec.Name, Err: err})
		}
	}
}

// Rebuild in configured order, never connection completion order. Previously
// returned tools and their definitions remain immutable while readers use them.
func (mgr *Manager) rebuildToolsLocked() {
	mgr.tools = nil
	mgr.warnings = make(map[string][]string)
	toolsByProjectedName := map[string]*MCPTool{}
	for index, server := range mgr.servers {
		if server.client == nil || mgr.awaitingHigherPriorityServer(index) {
			continue
		}
		spec, client, toolInfos := server.spec, server.client, server.listed
		key := spec.PluginID + "/" + spec.Name
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
	sort.SliceStable(mgr.tools, func(i, j int) bool { return mgr.tools[i].Definition().Name < mgr.tools[j].Definition().Name })
}

// A pending server can still own colliding projected tool names. Hold only
// overlapping namespaces until their priority is known, so a run never pins a
// temporary lower-priority callable. Independent namespaces publish immediately.
func (mgr *Manager) awaitingHigherPriorityServer(index int) bool {
	prefix := mcpToolNamePrefix(mgr.servers[index].spec.Name)
	for _, earlier := range mgr.servers[:index] {
		if earlier.client != nil || earlier.err != nil {
			continue
		}
		earlierPrefix := mcpToolNamePrefix(earlier.spec.Name)
		if strings.HasPrefix(prefix, earlierPrefix) || strings.HasPrefix(earlierPrefix, prefix) {
			return true
		}
	}
	return false
}

func mcpToolNamePrefix(serverName string) string {
	return strings.TrimSuffix(sanitizeToolName(serverName+"__tool"), "tool")
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

// Close cancels pending startup and waits for its producers before closing
// connected servers. No tools or failure callbacks are published after return.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.cancel()
		m.mu.Unlock()
		<-m.initialized
		m.mu.Lock()
		m.tools = nil
		m.mu.Unlock()
		for _, server := range m.servers {
			if server.client != nil {
				_ = server.client.Close()
			}
		}
	})
	return nil
}

func (m *Manager) GetServerInfos(pluginID string) []MCPServerInfo {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var infos []MCPServerInfo

	for _, server := range m.servers {
		if server.spec.PluginID != pluginID {
			continue
		}
		serverName, client := server.spec.Name, server.client
		key := pluginID + "/" + serverName
		status := "connecting"
		warnings := append([]string(nil), m.warnings[key]...)
		if server.err != nil {
			status = "failed"
		}
		if client != nil {
			status = "running"
			select {
			case <-client.closed:
				status = "failed"
			default:
			}
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
