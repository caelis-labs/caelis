package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// applicationCapabilities belongs to one admitted configuration revision. The
// ordinary and application paths share SDK MCP/Skill mechanics and the same
// Runtime; only Control's explicit selection and lifetime differ.
type applicationCapabilities struct {
	skillBundles []skill.PluginBundle
	skills       []sessionprofile.SkillStatus
	plugins      []sessionprofile.PluginStatus
	searchTool   tool.Tool
	source       tool.Source
}

type applicationMCPSource struct {
	selection mcp.Selection
	resolver  *applicationTurnResolver
}

// CheckSearchScope checks the durable profile before using the resident graph.
func (s applicationMCPSource) CheckSearchScope(ctx context.Context) error {
	return s.resolver.checkAppliedMCPConfiguration(ctx)
}

func (s applicationMCPSource) Tools() []tool.Tool {
	return s.leaseTools(s.selection.Tools())
}

func (s applicationMCPSource) leaseTools(ready []tool.Tool) []tool.Tool {
	for i, item := range ready {
		ready[i] = applicationMCPTool{Tool: item, source: s}
	}
	return ready
}

type applicationMCPTool struct {
	tool.Tool
	source applicationMCPSource
}

func (t applicationMCPTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := t.source.CheckSearchScope(ctx); err != nil {
		return tool.Result{}, err
	}
	return t.Tool.Call(ctx, call)
}

// New calls require a graph for the latest committed revision. Checking the
// applied revision rather than the request's original revision lets an
// unchanged binding continue after a successful reload.
func (r *applicationTurnResolver) checkAppliedMCPConfiguration(ctx context.Context) error {
	r.capabilitiesMu.Lock()
	revision, closed := r.desiredRevision, r.capabilitiesClosed
	r.capabilitiesMu.Unlock()
	if closed {
		return tool.NewError(tool.ErrorCodeStaleRevision, "MCP configuration is unavailable")
	}
	release, err := r.composition.authorities.sessionProfiles.GuardConfiguration(ctx, r.binding.Scope, r.binding.SessionID, revision)
	if errors.Is(err, sessionprofile.ErrConfigurationStale) {
		return tool.NewError(tool.ErrorCodeStaleRevision, "committed MCP configuration is awaiting reconciliation")
	}
	if err != nil {
		return err
	}
	release()
	return nil
}

func applicationSkillCatalog(profile sessionprofile.Profile) (skill.Catalog, error) {
	catalog, _, err := assembleApplicationSkills(profile, true)
	return catalog, err
}

// assembleApplicationSkills is the single scanner for admission and Runtime
// assembly. Admission rejects invalid selections; after commit, one damaged
// selected item is reported and omitted without suppressing healthy siblings.
func assembleApplicationSkills(profile sessionprofile.Profile, strict bool, bundles ...skill.PluginBundle) (skill.Catalog, []sessionprofile.SkillStatus, error) {
	var metas []skill.Meta
	statuses := make([]sessionprofile.SkillStatus, 0, len(profile.SkillDirs)+len(profile.SkillRoots))
	seen := map[string]bool{}
	addRoot := func(root string, direct bool) error {
		meta, err := skillfs.DiscoverRootMeta(root)
		if err != nil {
			if strict {
				// An arbitrary non-Skill subdirectory has never been part of
				// directory discovery. Explicit roots are always required.
				if !direct && errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return fmt.Errorf("application Skill root %q: %w", root, err)
			}
			statuses = append(statuses, sessionprofile.SkillStatus{Path: root, Kind: "skill", Status: "failed", Warning: applicationSkillWarning(err)})
			return nil
		}
		key := strings.ToLower(meta.Name)
		if seen[key] {
			if strict {
				return fmt.Errorf("application Skill name %q is duplicated", meta.Name)
			}
			statuses = append(statuses, sessionprofile.SkillStatus{Path: root, Kind: "skill", Name: meta.Name, Status: "failed", Warning: "Skill name conflicts with another selected Skill"})
			return nil
		}
		seen[key] = true
		metas = append(metas, meta)
		statuses = append(statuses, sessionprofile.SkillStatus{Path: root, Kind: "skill", Name: meta.Name, Status: "ready"})
		return nil
	}
	for _, dir := range profile.SkillDirs {
		info, err := os.Stat(dir)
		if err == nil && !info.IsDir() {
			err = fmt.Errorf("not a directory")
		}
		if err == nil {
			if _, rootErr := os.Lstat(filepath.Join(dir, "SKILL.md")); rootErr == nil {
				if err := addRoot(dir, true); err != nil {
					return skill.Catalog{}, nil, err
				}
				continue
			}
		}
		var entries []os.DirEntry
		if err == nil {
			entries, err = os.ReadDir(dir)
		}
		if err != nil {
			if strict {
				return skill.Catalog{}, nil, fmt.Errorf("application Skill directory %q: %w", dir, err)
			}
			statuses = append(statuses, sessionprofile.SkillStatus{Path: dir, Kind: "directory", Status: "failed", Warning: applicationSkillWarning(err)})
			continue
		}
		statuses = append(statuses, sessionprofile.SkillStatus{Path: dir, Kind: "directory", Status: "ready"})
		for _, entry := range entries {
			if entry.IsDir() {
				if err := addRoot(filepath.Join(dir, entry.Name()), false); err != nil {
					return skill.Catalog{}, nil, err
				}
			}
		}
	}
	for _, root := range profile.SkillRoots {
		if err := addRoot(root, true); err != nil {
			return skill.Catalog{}, nil, err
		}
	}
	pluginMetas, err := applicationPluginSkills(bundles)
	if err != nil {
		return skill.Catalog{}, nil, err
	}
	for _, regular := range metas {
		for _, bundle := range bundles {
			if plugin.PathWithinRoot(bundle.PluginRoot, regular.Path) {
				return skill.Catalog{}, nil, fmt.Errorf("native plugin Skill also selected by ordinary path")
			}
		}
	}
	for _, meta := range pluginMetas {
		if seen[strings.ToLower(meta.Name)] || seen[strings.ToLower(meta.LocalName)] {
			return skill.Catalog{}, nil, fmt.Errorf("selected Skill %q conflicts with a native plugin", meta.Name)
		}
		metas = append(metas, meta)
		statuses = append(statuses, sessionprofile.SkillStatus{Path: meta.Path, Kind: "skill", Name: meta.Name, Status: "ready"})
	}
	return skill.NewCatalog(metas), statuses, nil
}

func applicationSkillWarning(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "selected Skill path is missing"
	}
	return "selected Skill metadata is invalid or unreadable"
}

func (s *runtimeComposition) applicationSkills(profile sessionprofile.Profile, bundles ...skill.PluginBundle) (skill.Catalog, []sessionprofile.SkillStatus, error) {
	catalog, statuses, err := assembleApplicationSkills(profile, false, bundles...)
	if err != nil {
		return catalog, statuses, err
	}
	workspace := s.workspace.CWD
	metas, err := DiscoverSkillMetaRequest(skill.DiscoverRequest{
		Dirs: stackSkillDiscoveryDirs(workspace, s.runtimeProcessSnapshot().runtime.SkillDirs), WorkspaceDir: workspace, PluginBundles: bundles,
	})
	if err != nil {
		return skill.Catalog{}, nil, err
	}
	combined := catalog.Metas()
	for _, meta := range metas {
		if _, status := catalog.Resolve(meta.Name); meta.Source == skill.SourcePlugin || status != skill.ResolveNotFound {
			continue
		}
		combined = append(combined, meta)
		statuses = append(statuses, sessionprofile.SkillStatus{Path: meta.Path, Kind: "skill", Name: meta.Name, Status: "ready"})
	}
	return skill.NewCatalog(combined), statuses, nil
}

func applicationMCPSpecs(profile sessionprofile.Profile) []mcp.ServerSpec {
	specs := make([]mcp.ServerSpec, 0, len(profile.MCPServers))
	for _, server := range profile.MCPServers {
		specs = append(specs, mcp.ServerSpec{
			PluginID: "application", Name: server.Name, Transport: server.Transport,
			Command: server.Command, Args: append([]string(nil), server.Args...),
			WorkDir: server.WorkDir, URL: server.URL,
		})
	}
	return specs
}

func (r *applicationTurnResolver) capabilitiesFor(ctx context.Context, configuration sessionprofile.Configuration) (*applicationCapabilities, error) {
	r.capabilitiesMu.Lock()
	defer r.capabilitiesMu.Unlock()
	return r.reconcileCapabilitiesLocked(ctx, configuration)
}

func (r *applicationTurnResolver) currentCapabilities(ctx context.Context) (sessionprofile.Configuration, *applicationCapabilities, error) {
	for {
		configuration, err := r.composition.authorities.sessionProfiles.Configuration(ctx, r.binding.Scope, r.binding.SessionID)
		if err != nil {
			return sessionprofile.Configuration{}, nil, err
		}
		capabilities, err := r.capabilitiesFor(ctx, configuration)
		if !errors.Is(err, agent.ErrModelRequestSnapshotStale) {
			return configuration, capabilities, err
		}
		if err := ctx.Err(); err != nil {
			return sessionprofile.Configuration{}, nil, err
		}
	}
}

func (r *applicationTurnResolver) reconcileCapabilitiesLocked(ctx context.Context, configuration sessionprofile.Configuration) (*applicationCapabilities, error) {
	if r.capabilitiesClosed {
		return nil, fmt.Errorf("application capabilities are closed")
	}
	if configuration.Revision < r.desiredRevision {
		return nil, agent.ErrModelRequestSnapshotStale
	}
	if configuration.Revision == r.desiredRevision && r.capabilities != nil {
		return r.capabilities, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	contributions, err := r.composition.resolveAvailableApplicationPlugins(configuration.Profile, applicationPluginPaths(r.composition.authorities.storeDir, r.binding.SessionID, r.composition.workspace.CWD))
	if err != nil {
		return nil, err
	}
	_, skills, err := r.composition.applicationSkills(configuration.Profile, contributions.SkillBundles...)
	if err != nil {
		return nil, err
	}
	assembled := &applicationCapabilities{skillBundles: contributions.SkillBundles, skills: skills}
	r.composition.reportPluginFailures(contributions)
	for _, source := range contributions.Sources {
		assembled.plugins = append(assembled.plugins, pluginSourceStatus(source))
	}
	if err := r.reconcileMCPLocked(ctx, configuration, contributions); err != nil {
		return nil, err
	}
	r.bindApplicationMCP(assembled)
	r.capabilities = assembled
	return assembled, nil
}

func (r *applicationTurnResolver) liveSkillCatalog(ctx context.Context) (skill.Catalog, error) {
	configuration, capabilities, err := r.currentCapabilities(ctx)
	if err != nil {
		return skill.Catalog{}, err
	}
	catalog, statuses, err := r.composition.applicationSkills(configuration.Profile, capabilities.skillBundles...)
	if err != nil {
		return skill.Catalog{}, err
	}
	r.capabilitiesMu.Lock()
	if r.capabilities == capabilities {
		capabilities.skills = statuses
	}
	r.capabilitiesMu.Unlock()
	return catalog, nil
}

func (r *applicationTurnResolver) reconcileMCPLocked(ctx context.Context, configuration sessionprofile.Configuration, contributions plugin.Contributions) error {
	declared := append(applicationMCPSpecs(configuration.Profile), contributions.MCPServerSpecs...)
	specs := overlayApplicationMCP(r.inheritedMCP, declared)
	var err error
	if r.mcpManager == nil {
		r.mcpManager, err = mcp.NewManager(context.WithoutCancel(ctx), specs, nil)
	} else {
		err = r.mcpManager.Reconcile(specs)
	}
	if err != nil {
		return err
	}
	r.declaredMCP = declared
	r.desiredRevision = configuration.Revision
	r.failedMCPRevision = 0
	return nil
}

func (r *applicationTurnResolver) bindApplicationMCP(capabilities *applicationCapabilities) {
	capabilities.source = applicationMCPSource{selection: r.mcpManager.Selection(), resolver: r}
	capabilities.searchTool = toolsearch.NewSource(capabilities.source, newBoundToolSearchRanker(r.composition))
}

func (r *applicationTurnResolver) updateInheritedMCP(specs []mcp.ServerSpec) error {
	r.capabilitiesMu.Lock()
	defer r.capabilitiesMu.Unlock()
	if r.capabilitiesClosed {
		return nil
	}
	if err := r.mcpManager.Reconcile(overlayApplicationMCP(specs, r.declaredMCP)); err != nil {
		return err
	}
	r.inheritedMCP = specs
	if r.capabilities != nil {
		// In-flight requests keep the bindings they already received.
		updated := *r.capabilities
		r.bindApplicationMCP(&updated)
		r.capabilities = &updated
	}
	return nil
}

func overlayApplicationMCP(inherited, declared []mcp.ServerSpec) []mcp.ServerSpec {
	combined := append([]mcp.ServerSpec(nil), declared...)
	for _, server := range inherited {
		prefix := mcp.ServerToolNamePrefix(server.Name)
		shadowed := false
		for _, override := range declared {
			other := mcp.ServerToolNamePrefix(override.Name)
			if strings.HasPrefix(prefix, other) || strings.HasPrefix(other, prefix) {
				shadowed = true
				break
			}
		}
		if !shadowed {
			combined = append(combined, server)
		}
	}
	return combined
}

func (r *applicationTurnResolver) configurationCommitted(ctx context.Context, configuration sessionprofile.Configuration) {
	r.capabilitiesMu.Lock()
	defer r.capabilitiesMu.Unlock()
	if r.capabilitiesClosed || configuration.Revision <= r.desiredRevision {
		return
	}
	if err := r.reloadProfileMCPLocked(ctx, configuration); err != nil {
		r.failedMCPRevision = configuration.Revision
		if diagnostics := r.composition.authorities.diagnostics; diagnostics != nil {
			diagnostics.WarnContext(ctx, "Committed Session MCP configuration could not be reconciled", "session_id", r.binding.SessionID, "revision", configuration.Revision, "error", err)
		}
	}
}

func (r *applicationTurnResolver) reloadProfileMCPLocked(ctx context.Context, configuration sessionprofile.Configuration) error {
	if r.capabilitiesClosed {
		return fmt.Errorf("application capabilities are closed")
	}
	if configuration.Revision < r.desiredRevision {
		return sessionprofile.ErrConfigurationStale
	}
	contributions, err := r.composition.resolveAvailableApplicationPlugins(configuration.Profile, applicationPluginPaths(r.composition.authorities.storeDir, r.binding.SessionID, r.composition.workspace.CWD))
	if err != nil {
		return err
	}
	if err := r.reconcileMCPLocked(ctx, configuration, contributions); err != nil {
		return err
	}
	// Invalidate the old selection. The model-facing Skill list is pinned
	// separately in the Session's request instructions.
	r.capabilities = nil
	return nil
}

func (r *applicationTurnResolver) closeCapabilities() {
	r.capabilitiesMu.Lock()
	r.capabilitiesClosed = true
	manager := r.mcpManager
	r.mcpManager, r.capabilities = nil, nil
	r.capabilitiesMu.Unlock()
	_ = manager.Close()
}

func (r *applicationTurnResolver) capabilityStatus(revision uint64) sessionprofile.MCPStatus {
	r.capabilitiesMu.Lock()
	defer r.capabilitiesMu.Unlock()
	if revision != r.desiredRevision {
		if revision == r.failedMCPRevision {
			return sessionprofile.MCPStatus{Warning: "MCP configuration could not be applied. New calls are blocked pending reconciliation."}
		}
		return sessionprofile.MCPStatus{}
	}
	status := sessionprofile.MCPStatus{
		Servers: mcpServerStatuses(r.mcpManager),
	}
	if r.capabilities != nil {
		status.Skills = append([]sessionprofile.SkillStatus(nil), r.capabilities.skills...)
		status.Plugins = append([]sessionprofile.PluginStatus(nil), r.capabilities.plugins...)
	}
	return status
}

func mcpServerStatuses(manager *mcp.Manager) []sessionprofile.MCPServerStatus {
	var servers []sessionprofile.MCPServerStatus
	for _, info := range manager.GetServerInfos("") {
		server := applicationMCPServerStatus(info)
		if info.PluginID != "application" {
			server.PluginID = info.PluginID
		}
		servers = append(servers, server)
	}
	return servers
}

func applicationMCPServerStatus(info mcp.MCPServerInfo) sessionprofile.MCPServerStatus {
	return sessionprofile.MCPServerStatus{
		Name: info.Name, Status: info.Status, Tools: info.Tools, ToolDetails: info.ToolDetails, Warning: info.Warning,
		Ready: info.Ready, Refreshing: info.Refreshing, Stale: info.Stale, RefreshFailed: info.RefreshFailed,
	}
}

// Refresh an already-applied directory, or reconcile the current profile.
func (r *applicationTurnResolver) refreshMCPTools(ctx context.Context, pluginID, serverName string) error {
	store := r.composition.authorities.sessionProfiles
	if err := store.CheckActive(ctx, r.binding.Scope); err != nil {
		return err
	}
	configuration, err := store.Configuration(ctx, r.binding.Scope, r.binding.SessionID)
	if err != nil {
		return err
	}
	r.capabilitiesMu.Lock()
	manager := r.mcpManager
	if r.capabilitiesClosed || manager == nil || configuration.Revision != r.desiredRevision {
		r.capabilitiesMu.Unlock()
		return fmt.Errorf("active MCP directory is unavailable")
	}
	r.capabilitiesMu.Unlock()
	return manager.RefreshTools(ctx, pluginID, serverName)
}

func (s applicationMCPSource) SearchTools() []tool.Tool {
	return s.leaseTools(s.selection.SearchTools())
}

func (s applicationMCPSource) CheckToolBinding(ctx context.Context, binding tool.Binding) error {
	if err := s.CheckSearchScope(ctx); err != nil {
		return err
	}
	return s.selection.CheckToolBinding(ctx, binding)
}
