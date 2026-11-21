package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPServerInfo struct {
	PluginID string
	Name     string
	Status   string
	// Ready reports whether the current connection has a dispatchable directory.
	Ready       bool
	Tools       []string
	ToolDetails []MCPToolDetail
	Warning     string
	// Refreshing, Stale and RefreshFailed are independent directory facts.
	// Status continues to describe the connection lifecycle only.
	Refreshing bool
	Stale      bool
	// RefreshFailed reports a settled failure for the current catalog
	// generation, not an outdated attempt.
	RefreshFailed bool
}

// MCPToolDetail describes one accepted tool in the current ready Runtime catalog.
// Name is the remote name also present in MCPServerInfo.Tools; Description is
// the bounded, non-authorizing description in the callable definition.
type MCPToolDetail struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ServerFailure describes one failed initialization attempt. Err is private
// diagnostic data; presentation adapters must supply bounded public wording.
type ServerFailure struct {
	PluginID string
	Name     string
	Err      error
}

type managedServer struct {
	spec         ServerSpec
	cancel       context.CancelFunc
	inFlight     int
	drained      chan struct{}
	client       *Client
	listed       []*mcpsdk.Tool
	err          error
	refreshGate  chan struct{} // capacity 1; sole refresh ownership
	revoked      bool
	catalogReady bool
	refreshErr   error
	// refreshing and refreshErrGeneration are guarded by Manager.mu; the
	// generation records which directory revision the attempt covered.
	refreshing           bool
	refreshErrGeneration uint64
}

// Manager owns background MCP initialization and immutable ready-tool snapshots.
type Manager struct {
	mu             sync.Mutex
	servers        []*managedServer
	tools          []tool.Tool
	toolsByName    map[string]*MCPTool
	warnings       map[string][]string
	cancel         context.CancelFunc
	ctx            context.Context
	start          clientStarter
	initialized    chan struct{}
	closed         bool
	closeOnce      sync.Once
	refreshWorkers sync.WaitGroup
	workers        sync.WaitGroup
	owners         map[string]string
	nextBinding    uint64
	onFailure      func(ServerFailure)
}

// Initialized closes after the initial set of servers has initialized or failed.
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

// NewManager starts each server independently in the background. Connection or
// listing failures affect only that server and invoke onFailure once, if set.
// The manager refreshes ready catalogs when servers report changes.
// ctx owns initialization; Close cancels and drains all initialization work.
func NewManager(ctx context.Context, specs []ServerSpec, onFailure func(ServerFailure)) (*Manager, error) {
	return newManager(ctx, specs, func(ctx context.Context, spec ServerSpec) (*Client, error) {
		return startClient(ctx, spec, nil)
	}, onFailure)
}

type clientStarter func(context.Context, ServerSpec) (*Client, error)

func newManager(ctx context.Context, specs []ServerSpec, start clientStarter, onFailure func(ServerFailure)) (*Manager, error) {
	ctx, cancel := context.WithCancel(ctx)
	mgr := &Manager{warnings: make(map[string][]string), ctx: ctx, cancel: cancel, start: start, initialized: make(chan struct{}), onFailure: onFailure}
	var initial sync.WaitGroup
	if err := mgr.reconcile(specs, &initial); err != nil {
		cancel()
		return nil, err
	}
	go func() { initial.Wait(); close(mgr.initialized) }()
	return mgr, nil
}

// Reconcile applies a complete desired connection set. Equal specs keep their
// clients, tool bindings and revocation state. Replaced or removed clients
// drain dispatched calls while new clients connect independently.
func (m *Manager) Reconcile(specs []ServerSpec) error {
	return m.reconcile(specs, nil)
}

func (m *Manager) reconcile(specs []ServerSpec, initial *sync.WaitGroup) error {
	seen := make(map[string]bool, len(specs))
	for _, spec := range specs {
		if err := ValidateServerIdentity(spec); err != nil {
			return err
		}
		key := spec.PluginID + "/" + spec.Name
		if seen[key] {
			return fmt.Errorf("mcp manager: duplicate server %s", key)
		}
		seen[key] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return errors.New("MCP manager is closed")
	}
	current := make(map[string]*managedServer, len(m.servers))
	for _, server := range m.servers {
		current[server.spec.PluginID+"/"+server.spec.Name] = server
	}
	next := make([]*managedServer, 0, len(specs))
	changed := len(m.servers) != len(specs)
	for _, spec := range specs {
		spec.Args = append([]string(nil), spec.Args...)
		spec.ReplaySourceIDs = append([]string(nil), spec.ReplaySourceIDs...)
		spec.PrefixTools = append([]string(nil), spec.PrefixTools...)
		spec.Env = maps.Clone(spec.Env)
		spec.Headers = maps.Clone(spec.Headers)
		key := spec.PluginID + "/" + spec.Name
		old := current[key]
		delete(current, key)
		if old != nil && reflect.DeepEqual(old.spec, spec) {
			changed = changed || old != m.servers[len(next)]
			next = append(next, old)
			continue
		}
		changed = true
		if old != nil {
			m.retireLocked(old)
		}
		ctx, cancel := context.WithCancel(m.ctx)
		server := &managedServer{spec: spec, cancel: cancel, refreshGate: make(chan struct{}, 1)}
		next = append(next, server)
		if initial != nil {
			initial.Add(1)
		}
		m.workers.Go(func() {
			if initial != nil {
				defer initial.Done()
			}
			m.initialize(ctx, server, m.start)
		})
	}
	for key, server := range current {
		m.retireLocked(server)
		for name, owner := range m.owners {
			if strings.HasPrefix(owner, key+"/") {
				delete(m.owners, name)
			}
		}
	}
	if changed {
		m.servers = next
		m.rebuildToolsLocked()
	}
	return nil
}

// borrowClient admits the exact callable before counting it as in flight. The
// locks order admission with catalog invalidation, retirement, and published
// closure; none are held across MCP I/O.
func (m *Manager) borrowClient(ctx context.Context, remote *MCPTool) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	server := remote.server
	server.client.catalog.mu.Lock()
	defer server.client.catalog.mu.Unlock()
	if err := m.checkServerReadyLocked(ctx, server); err != nil {
		return nil, err
	}
	if current := m.toolsByName[remote.def.Name]; current == nil || current.bindingID != remote.bindingID {
		return nil, tool.NewError(tool.ErrorCodeStaleRevision, "MCP directory refreshed; callable source or binding is stale")
	}
	server.inFlight++
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		server.inFlight--
		if server.inFlight == 0 && server.drained != nil {
			close(server.drained)
			server.drained = nil
		}
	}, nil
}

// retireLocked stops admission before waiting for calls which already borrowed
// the client. A startup that has not published its client cleans up itself.
func (m *Manager) retireLocked(server *managedServer) {
	server.revoked = true
	server.cancel()
	client := server.client
	if client == nil {
		return
	}
	var drained <-chan struct{}
	if server.inFlight != 0 {
		server.drained = make(chan struct{})
		drained = server.drained
	}
	m.workers.Go(func() {
		if drained != nil {
			select {
			case <-drained:
			case <-m.ctx.Done():
			}
		}
		_ = client.Close()
	})
}

func (m *Manager) initialize(parent context.Context, server *managedServer, start clientStarter) {
	ctx, cancel := context.WithTimeout(parent, DefaultStartupTimeout)
	client, err := start(ctx, server.spec)
	if err == nil {
		err = ctx.Err()
	}
	cancel()
	if err != nil {
		var phase *startupError
		if !errors.As(err, &phase) {
			err = &startupError{Stage: "connection", Err: err}
		}
	}
	m.mu.Lock()
	if m.closed || server.revoked || parent.Err() != nil {
		m.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		return
	}
	server.err = err
	if err == nil {
		server.client = client
	}
	m.rebuildToolsLocked()
	m.mu.Unlock()
	if err == nil {
		refreshErr := m.refreshCatalog(parent, server, false)
		if refreshErr != nil {
			refreshErr = client.startupFailure("tools/list", refreshErr)
		}
		m.mu.Lock()
		if m.closed || server.revoked || parent.Err() != nil {
			m.mu.Unlock()
			_ = client.Close()
			return
		}
		server.err = refreshErr
		server.catalogReady = true
		m.rebuildToolsLocked()
		m.mu.Unlock()
		m.refreshWorkers.Go(func() { m.watchCatalog(parent, server) })
		if refreshErr != nil && m.onFailure != nil {
			m.onFailure(ServerFailure{PluginID: server.spec.PluginID, Name: server.spec.Name, Err: refreshErr})
		}
	}
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
		if server.client == nil || server.revoked || mgr.awaitingHigherPriorityServer(index) {
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
		var implementation *mcpsdk.Implementation
		var protocolVersion string
		if client.session != nil {
			if initialized := client.session.InitializeResult(); initialized != nil {
				implementation, protocolVersion = initialized.ServerInfo, initialized.ProtocolVersion
			}
		}
		identitySpec := spec
		if client.transport == TransportStdio && client.launchIdentity != "" {
			// The effective startup snapshot, not the pre-merge Env map or
			// clean/inherit flag, identifies the environment the child saw.
			identitySpec.Env = nil
			identitySpec.CleanEnvironment = false
		}
		for _, info := range toolInfos {
			if info == nil || strings.TrimSpace(info.Name) == "" {
				continue
			}
			toolLabel := mcpWarningToolName(info.Name)
			if err := validateMCPIdentity("remote tool name", info.Name, maxMCPRemoteToolNameRunes); err != nil {
				mgr.addWarning(key, fmt.Sprintf("tool %s quarantined: %v", toolLabel, err))
				continue
			}
			name := formatToolName(spec.Name, info.Name)
			owner := key + "/" + info.Name
			if pinned := mgr.owners[name]; pinned != "" && pinned != owner {
				continue
			}
			sourceBytes, err := json.Marshal(struct {
				Server          ServerSpec
				LaunchIdentity  string
				Implementation  *mcpsdk.Implementation
				ProtocolVersion string
				Tool            *mcpsdk.Tool
			}{identitySpec, client.launchIdentity, implementation, protocolVersion, info})
			if err != nil {
				mgr.addWarning(key, fmt.Sprintf("tool %s quarantined: cannot identify source: %v", toolLabel, err))
				continue
			}
			sourceHash := sha256.Sum256(sourceBytes)
			if toolsByProjectedName[name] != nil {
				continue
			}
			def := tool.Definition{
				Name:        name,
				Description: info.Description,
				Metadata: map[string]any{
					MetadataKind:              KindTool,
					MetadataPluginID:          spec.PluginID,
					MetadataServer:            spec.Name,
					MetadataTool:              info.Name,
					MetadataSourceFingerprint: hex.EncodeToString(sourceHash[:]),
				},
			}
			if slices.Contains(spec.PrefixTools, info.Name) {
				def.Metadata[MetadataModelFunction] = true
			}
			if info.OutputSchema != nil {
				def.OutputSchema, err = normalizeMCPInputSchema(info.OutputSchema)
				if err != nil {
					mgr.addWarning(key, fmt.Sprintf("tool %s quarantined: output schema: %v", toolLabel, err))
					continue
				}
			}
			def, warning, err := normalizeListedToolDefinition(def, info.InputSchema)
			if warning != "" {
				mgr.addWarning(key, fmt.Sprintf("tool %s: %s", toolLabel, warning))
			}
			if err != nil {
				mgr.addWarning(key, fmt.Sprintf("tool %s quarantined: %v", toolLabel, err))
				continue
			}
			if mgr.owners == nil {
				mgr.owners = map[string]string{}
			}
			mgr.owners[name] = owner
			t := &MCPTool{
				client:   client,
				manager:  mgr,
				server:   server,
				origName: info.Name,
				def:      def,
			}
			// Carry the admission ID only across consecutive publications of
			// this exact entry. Removal and later re-addition cannot revive it,
			// even if the definition's durable fingerprint matches again.
			if old := mgr.toolsByName[name]; old != nil && old.server == server && old.client == client && old.origName == info.Name &&
				old.def.Metadata[MetadataSourceFingerprint] == def.Metadata[MetadataSourceFingerprint] {
				t.bindingID = old.bindingID
			} else {
				mgr.nextBinding++
				t.bindingID = mgr.nextBinding
			}
			toolsByProjectedName[name] = t
			mgr.tools = append(mgr.tools, t)
		}
	}
	sort.SliceStable(mgr.tools, func(i, j int) bool { return mgr.tools[i].Definition().Name < mgr.tools[j].Definition().Name })
	// This index is derived from the same immutable canonical directory and is
	// published under the same owner lock. Readiness is checked at each lookup.
	mgr.toolsByName = toolsByProjectedName

}

// A pending server can still own colliding projected tool names. Hold only
// overlapping namespaces until their priority is known, so a run never pins a
// temporary lower-priority callable. Independent namespaces publish immediately.
func (mgr *Manager) awaitingHigherPriorityServer(index int) bool {
	prefix := ServerToolNamePrefix(mgr.servers[index].spec.Name)
	for _, earlier := range mgr.servers[:index] {
		if (earlier.client != nil && earlier.catalogReady) || earlier.err != nil {
			continue
		}
		earlierPrefix := ServerToolNamePrefix(earlier.spec.Name)
		if strings.HasPrefix(prefix, earlierPrefix) || strings.HasPrefix(earlierPrefix, prefix) {
			return true
		}
	}
	return false
}

// ServerToolNamePrefix is the canonical projected namespace for a server.
// Host admission can reject ambiguous explicit selections before starting clients.
func ServerToolNamePrefix(serverName string) string {
	return strings.TrimSuffix(sanitizeToolName(serverName+"__tool"), "tool")
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
	// Keep the original priority winner pinned for this Runtime. A failed
	// process removes its callable without silently promoting another server
	// under the same projected name or restarting the original process.
	var ready []tool.Tool
	for _, candidate := range m.tools {
		if remote, ok := candidate.(*MCPTool); ok && remote.client.closedError() != nil {
			continue
		}
		ready = append(ready, candidate)
	}
	return ready
}

// PrefixTools returns ready callables selected by their own connection spec.
func (m *Manager) PrefixTools() []tool.Tool {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var selected []tool.Tool
	for _, server := range m.servers {
		if len(server.spec.PrefixTools) == 0 || server.client == nil || m.checkCatalogReadyLocked(context.Background(), server) != nil {
			continue
		}
		for _, method := range server.spec.PrefixTools {
			if candidate := m.toolsByName[formatToolName(server.spec.Name, method)]; candidate != nil &&
				candidate.server == server && candidate.origName == method {
				selected = append(selected, candidate)
			}
		}
	}
	return selected
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
		m.workers.Wait()
		m.refreshWorkers.Wait()
		m.mu.Lock()
		m.tools = nil
		m.toolsByName = nil
		m.mu.Unlock()
		for _, server := range m.servers {
			if server.client != nil {
				_ = server.client.Close()
			}
		}
	})
	return nil
}

// GetServerInfos observes the specified namespace, or all servers when empty.
func (m *Manager) GetServerInfos(pluginID string) []MCPServerInfo {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var infos []MCPServerInfo

	for index, server := range m.servers {
		if pluginID != "" && server.spec.PluginID != pluginID {
			continue
		}
		serverName, client := server.spec.Name, server.client
		key := server.spec.PluginID + "/" + serverName
		status := "connecting"
		warnings := append([]string(nil), m.warnings[key]...)
		if server.refreshErr != nil {
			warnings = append(warnings, "catalog refresh failed; previous directory retained, new calls blocked")
		}
		if server.err != nil {
			status = "failed"
			warnings = append(warnings, startupFailureSummary(server.err))
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
		var details []MCPToolDetail
		stale := false
		var generation uint64
		if client != nil {
			if c := client.catalog; c != nil {
				c.mu.Lock()
				stale, generation = c.dirty, c.generation
				c.mu.Unlock()
			} else {
				// No catalog yet: the directory is not verified.
				stale = true
			}
		}
		if status == "running" {
			for _, t := range m.tools {
				if mcpTool, ok := t.(*MCPTool); ok && mcpTool.client == client {
					tools = append(tools, mcpTool.origName)
					details = append(details, MCPToolDetail{Name: mcpTool.origName, Description: mcpTool.def.Description})
				}
			}
		}
		sort.Strings(tools)
		sort.Slice(details, func(i, j int) bool { return details[i].Name < details[j].Name })

		infos = append(infos, MCPServerInfo{
			PluginID:      server.spec.PluginID,
			Name:          serverName,
			Status:        status,
			Ready:         status == "running" && server.catalogReady && !server.revoked && !stale && server.err == nil && !m.awaitingHigherPriorityServer(index),
			Tools:         tools,
			ToolDetails:   details,
			Warning:       strings.Join(warnings, "; "),
			Refreshing:    server.refreshing,
			Stale:         stale,
			RefreshFailed: settledRefreshFailed(server, generation),
		})
	}
	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].Name < infos[j].Name
	})
	return infos
}

// SearchTools returns only currently dispatchable directory candidates. Status
// and refresh diagnostics retain the last whole directory through Tools.
func (m *Manager) SearchTools() []tool.Tool {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var ready []tool.Tool
	for _, one := range m.tools {
		if remote, ok := one.(*MCPTool); ok && m.checkCatalogReadyLocked(context.Background(), remote.server) == nil {
			ready = append(ready, one)
		}
	}
	return ready
}
