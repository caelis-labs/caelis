package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	skilltool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// applicationCapabilities belongs to one admitted configuration revision. The
// ordinary and application paths share SDK MCP/Skill mechanics and the same
// Runtime; only Control's explicit selection and lifetime differ.
type applicationCapabilities struct {
	catalog    skill.Catalog
	skills     []sessionprofile.SkillStatus
	plugins    []sessionprofile.PluginStatus
	skillTool  tool.Tool
	searchTool tool.Tool
	source     tool.Source
	resource   *applicationMCPResource
}

// The resolver owns the current MCP configuration; each model snapshot also
// holds a reference until its response and tool calls have completed.
type applicationMCPResource struct {
	servers []sessionprofile.MCPServer
	plugins []sessionprofile.Plugin
	specs   []mcp.ServerSpec
	manager *mcp.Manager
	refs    int
	retired bool
	closed  bool
}

func (r *applicationMCPResource) matches(profile sessionprofile.Profile) bool {
	return reflect.DeepEqual(r.servers, profile.MCPServers) && reflect.DeepEqual(r.plugins, profile.Plugins)
}

type applicationMCPSource struct {
	manager   *mcp.Manager
	store     *sessionprofile.Store
	scope     sessionprofile.Scope
	sessionID string
	revision  uint64
}

// CheckSearchScope closes schema reads and result publication when a lease is
// revoked or this Application's desired catalog revision has changed.
func (s applicationMCPSource) CheckSearchScope(ctx context.Context) error {
	if err := s.store.CheckActive(ctx, s.scope); err != nil {
		return err
	}
	current, err := s.store.Configuration(ctx, s.scope, s.sessionID)
	if err != nil {
		return err
	}
	if current.Revision != s.revision {
		return fmt.Errorf("ToolSearch Application configuration changed: %w", sessionprofile.ErrConfigurationStale)
	}
	return nil
}

func (s applicationMCPSource) Tools() []tool.Tool {
	ready := s.manager.Tools()
	for i, item := range ready {
		ready[i] = applicationLeasedTool{Tool: item, store: s.store, scope: s.scope}
	}
	return ready
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

func applicationSkillMetadata(catalog skill.Catalog) string {
	metas := catalog.Metas()
	if len(metas) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Skills\nUse the Skill tool to load a matching skill before following its instructions. Only the Skill tool result contains its body.\n### Available skills\n")
	for _, meta := range metas {
		fmt.Fprintf(&b, "- %s: %s\n", strings.Join(strings.Fields(meta.Name), " "), strings.Join(strings.Fields(meta.Description), " "))
	}
	return strings.TrimSpace(b.String())
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

func (r *applicationTurnResolver) acquireCapabilities(ctx context.Context, configuration sessionprofile.Configuration) (*applicationCapabilities, func(), error) {
	r.capabilitiesMu.Lock()
	defer r.capabilitiesMu.Unlock()
	if r.capabilitiesClosed {
		return nil, nil, fmt.Errorf("application capabilities are closed")
	}
	var obsolete *mcp.Manager
	if configuration.Revision > r.desiredRevision {
		obsolete = r.setDesiredCapabilitiesLocked(configuration)
	}
	if obsolete != nil {
		// No admitted snapshot references this manager. Closing while holding
		// the resolver lock keeps a later acquisition from racing its shutdown.
		_ = obsolete.Close()
	}
	if existing := r.capabilities[configuration.Revision]; existing != nil {
		return existing, r.retainCapabilitiesLocked(existing), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	contributions, err := applicationPluginContributions(configuration.Profile, applicationPluginPaths(r.composition.authorities.storeDir, r.binding.SessionID, r.composition.workspace.CWD))
	if err != nil {
		return nil, nil, err
	}
	catalog, skills, err := assembleApplicationSkills(configuration.Profile, false, contributions.SkillBundles...)
	if err != nil {
		return nil, nil, err
	}
	assembled := &applicationCapabilities{catalog: catalog, skills: skills}
	for _, source := range contributions.Sources {
		assembled.plugins = append(assembled.plugins, sessionprofile.PluginStatus{ID: source.ID, Root: source.Root, Version: source.Version, Status: "assembled"})
	}
	specs := append(applicationMCPSpecs(configuration.Profile), contributions.MCPServerSpecs...)
	if len(catalog.Metas()) != 0 {
		assembled.skillTool = skilltool.New(skilltool.Config{Loader: skillfs.Loader{}, Catalog: catalog})
	}
	if len(specs) != 0 {
		resource := r.currentMCP
		if resource == nil || !reflect.DeepEqual(resource.specs, specs) {
			if resource != nil && configuration.Revision >= r.desiredRevision {
				r.currentMCP = nil
				resource.retired = true
				if resource.refs == 0 && !resource.closed {
					resource.closed = true
					_ = resource.manager.Close()
				}
			}
			manager, err := mcp.NewManagerWithOptions(context.WithoutCancel(ctx), specs, nil, mcp.ManagerOptions{})
			if err != nil {
				return nil, nil, err
			}
			resource = &applicationMCPResource{servers: append([]sessionprofile.MCPServer(nil), configuration.Profile.MCPServers...), plugins: configuration.Profile.Plugins, specs: specs, manager: manager, retired: configuration.Revision < r.desiredRevision}
			if !resource.retired {
				r.currentMCP = resource
			}
		}
		assembled.resource = resource
		assembled.source = applicationMCPSource{manager: resource.manager, store: r.composition.authorities.sessionProfiles, scope: r.binding.Scope, sessionID: r.binding.SessionID, revision: configuration.Revision}
		assembled.searchTool = toolsearch.NewSource(assembled.source, newBoundToolSearchRanker(r.composition))
	}
	if configuration.Revision >= r.desiredRevision && r.capabilities == nil {
		r.capabilities = map[uint64]*applicationCapabilities{}
	}
	if configuration.Revision >= r.desiredRevision {
		r.capabilities[configuration.Revision] = assembled
	}
	return assembled, r.retainCapabilitiesLocked(assembled), nil
}

func (r *applicationTurnResolver) retainCapabilitiesLocked(capability *applicationCapabilities) func() {
	if capability.resource == nil {
		return func() {}
	}
	resource := capability.resource
	resource.refs++
	return func() {
		r.capabilitiesMu.Lock()
		resource.refs--
		closeManager := resource.retired && resource.refs == 0 && !resource.closed
		if closeManager {
			resource.closed = true
			// Serialize shutdown with activation close and new acquisitions.
			_ = resource.manager.Close()
		}
		r.capabilitiesMu.Unlock()
	}
}

func (r *applicationTurnResolver) setDesiredCapabilitiesLocked(configuration sessionprofile.Configuration) *mcp.Manager {
	r.desiredRevision = configuration.Revision
	for revision := range r.capabilities {
		if revision < configuration.Revision {
			delete(r.capabilities, revision)
		}
	}
	current := r.currentMCP
	if current == nil || current.matches(configuration.Profile) {
		return nil
	}
	r.currentMCP = nil
	current.retired = true
	if current.refs == 0 {
		current.closed = true
		return current.manager
	}
	return nil
}

func (r *applicationTurnResolver) configurationCommitted(configuration sessionprofile.Configuration) {
	r.capabilitiesMu.Lock()
	if !r.capabilitiesClosed && configuration.Revision > r.desiredRevision {
		if obsolete := r.setDesiredCapabilitiesLocked(configuration); obsolete != nil {
			_ = obsolete.Close()
		}
	}
	r.capabilitiesMu.Unlock()
}

func (r *applicationTurnResolver) closeCapabilities() {
	r.capabilitiesMu.Lock()
	r.capabilitiesClosed = true
	r.capabilities = nil
	current := r.currentMCP
	r.currentMCP = nil
	if current != nil {
		current.retired = true
	}
	closeManager := current != nil && current.refs == 0 && !current.closed
	if closeManager {
		current.closed = true
		_ = current.manager.Close()
	}
	r.capabilitiesMu.Unlock()
}

func (r *applicationTurnResolver) capabilityStatus(revision uint64) sessionprofile.MCPStatus {
	r.capabilitiesMu.Lock()
	defer r.capabilitiesMu.Unlock()
	capability := r.capabilities[revision]
	current := r.currentMCP
	status := sessionprofile.MCPStatus{}
	if capability != nil {
		current = capability.resource
		status.Skills = append([]sessionprofile.SkillStatus(nil), capability.skills...)
		status.Plugins = append([]sessionprofile.PluginStatus(nil), capability.plugins...)
	} else if revision != r.desiredRevision {
		current = nil
	}
	if current == nil {
		return status
	}
	seen := map[string]bool{}
	for _, spec := range current.specs {
		if seen[spec.PluginID] {
			continue
		}
		seen[spec.PluginID] = true
		for _, info := range current.manager.GetServerInfos(spec.PluginID) {
			server := applicationMCPServerStatus(info)
			if spec.PluginID != "application" {
				server.PluginID = spec.PluginID
			}
			status.Servers = append(status.Servers, server)
		}
	}
	return status
}

func applicationMCPServerStatus(info mcp.MCPServerInfo) sessionprofile.MCPServerStatus {
	return sessionprofile.MCPServerStatus{
		Name: info.Name, Status: info.Status, Tools: info.Tools, ToolDetails: info.ToolDetails, Warning: info.Warning,
		Refreshing: info.Refreshing, Stale: info.Stale, RefreshFailed: info.RefreshFailed,
	}
}

// Borrow only the manager already owned by this activation. A remote retry must
// not start a dormant catalog or resurrect one retired by configuration removal.
func (r *applicationTurnResolver) refreshMCPTools(ctx context.Context, pluginID, serverName string) error {
	if err := r.composition.authorities.sessionProfiles.CheckActive(ctx, r.binding.Scope); err != nil {
		return err
	}
	configuration, err := r.composition.authorities.sessionProfiles.Configuration(ctx, r.binding.Scope, r.binding.SessionID)
	if err != nil {
		return err
	}
	r.capabilitiesMu.Lock()
	current := r.currentMCP
	if r.capabilitiesClosed || current == nil || current.retired || configuration.Revision != r.desiredRevision || !current.matches(configuration.Profile) {
		r.capabilitiesMu.Unlock()
		return fmt.Errorf("active MCP directory is unavailable")
	}
	release := r.retainCapabilitiesLocked(&applicationCapabilities{resource: current})
	r.capabilitiesMu.Unlock()
	defer release()
	return current.manager.RefreshTools(ctx, pluginID, serverName)
}

func (s applicationMCPSource) SearchTools() []tool.Tool {
	ready := s.manager.SearchTools()
	for i, item := range ready {
		ready[i] = applicationLeasedTool{Tool: item, store: s.store, scope: s.scope}
	}
	return ready
}
