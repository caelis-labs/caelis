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
	"github.com/caelis-labs/caelis/control/application"
)

// applicationCapabilities belongs to one admitted configuration revision. The
// ordinary and application paths share SDK MCP/Skill mechanics and the same
// Runtime; only Control's explicit selection and lifetime differ.
type applicationCapabilities struct {
	catalog    skill.Catalog
	skills     []application.SkillStatus
	skillTool  tool.Tool
	searchTool tool.Tool
	source     tool.Source
	resource   *applicationMCPResource
}

// The resolver owns the current MCP configuration; each model snapshot also
// holds a reference until its response and tool calls have completed.
type applicationMCPResource struct {
	servers []application.MCPServer
	manager *mcp.Manager
	refs    int
	retired bool
	closed  bool
}

type applicationMCPSource struct {
	manager   *mcp.Manager
	store     *application.Store
	scope     application.Scope
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
		return fmt.Errorf("ToolSearch Application configuration changed: %w", application.ErrConfigurationStale)
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

func applicationSkillCatalog(profile application.Profile) (skill.Catalog, error) {
	catalog, _, err := assembleApplicationSkills(profile, true)
	return catalog, err
}

// assembleApplicationSkills is the single scanner for admission and Runtime
// assembly. Admission rejects invalid selections; after commit, one damaged
// selected item is reported and omitted without suppressing healthy siblings.
func assembleApplicationSkills(profile application.Profile, strict bool) (skill.Catalog, []application.SkillStatus, error) {
	var metas []skill.Meta
	statuses := make([]application.SkillStatus, 0, len(profile.SkillDirs)+len(profile.SkillRoots))
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
			statuses = append(statuses, application.SkillStatus{Path: root, Kind: "skill", Status: "failed", Warning: applicationSkillWarning(err)})
			return nil
		}
		key := strings.ToLower(meta.Name)
		if seen[key] {
			if strict {
				return fmt.Errorf("application Skill name %q is duplicated", meta.Name)
			}
			statuses = append(statuses, application.SkillStatus{Path: root, Kind: "skill", Name: meta.Name, Status: "failed", Warning: "Skill name conflicts with another selected Skill"})
			return nil
		}
		seen[key] = true
		metas = append(metas, meta)
		statuses = append(statuses, application.SkillStatus{Path: root, Kind: "skill", Name: meta.Name, Status: "ready"})
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
			statuses = append(statuses, application.SkillStatus{Path: dir, Kind: "directory", Status: "failed", Warning: applicationSkillWarning(err)})
			continue
		}
		statuses = append(statuses, application.SkillStatus{Path: dir, Kind: "directory", Status: "ready"})
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

func applicationMCPSpecs(profile application.Profile) []mcp.ServerSpec {
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

func (r *applicationTurnResolver) acquireCapabilities(ctx context.Context, configuration application.Configuration) (*applicationCapabilities, func(), error) {
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
	catalog, skills, err := assembleApplicationSkills(configuration.Profile, false)
	if err != nil {
		return nil, nil, err
	}
	assembled := &applicationCapabilities{catalog: catalog, skills: skills}
	if len(catalog.Metas()) != 0 {
		assembled.skillTool = skilltool.New(skilltool.Config{Loader: skillfs.Loader{}, Catalog: catalog})
	}
	if len(configuration.Profile.MCPServers) != 0 {
		resource := r.currentMCP
		if resource == nil || !reflect.DeepEqual(resource.servers, configuration.Profile.MCPServers) {
			manager, err := mcp.NewManager(context.WithoutCancel(ctx), applicationMCPSpecs(configuration.Profile), nil)
			if err != nil {
				return nil, nil, err
			}
			resource = &applicationMCPResource{servers: append([]application.MCPServer(nil), configuration.Profile.MCPServers...), manager: manager, retired: configuration.Revision < r.desiredRevision}
			if !resource.retired {
				r.currentMCP = resource
			}
		}
		assembled.resource = resource
		assembled.source = applicationMCPSource{manager: resource.manager, store: r.composition.authorities.applications, scope: r.binding.Scope, sessionID: r.binding.SessionID, revision: configuration.Revision}
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

func (r *applicationTurnResolver) setDesiredCapabilitiesLocked(configuration application.Configuration) *mcp.Manager {
	r.desiredRevision = configuration.Revision
	for revision := range r.capabilities {
		if revision < configuration.Revision {
			delete(r.capabilities, revision)
		}
	}
	current := r.currentMCP
	if current == nil || reflect.DeepEqual(current.servers, configuration.Profile.MCPServers) {
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

func (r *applicationTurnResolver) configurationCommitted(configuration application.Configuration) {
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

func (r *applicationTurnResolver) capabilityStatus(revision uint64) application.MCPStatus {
	r.capabilitiesMu.Lock()
	defer r.capabilitiesMu.Unlock()
	capability := r.capabilities[revision]
	current := r.currentMCP
	status := application.MCPStatus{}
	if capability != nil {
		current = capability.resource
		status.Skills = append([]application.SkillStatus(nil), capability.skills...)
	} else if revision != r.desiredRevision {
		current = nil
	}
	if current == nil {
		return status
	}
	infos := current.manager.GetServerInfos("application")
	status.Servers = make([]application.MCPServerStatus, 0, len(infos))
	for _, info := range infos {
		status.Servers = append(status.Servers, application.MCPServerStatus{Name: info.Name, Status: info.Status, Tools: info.Tools, ToolDetails: info.ToolDetails, Warning: info.Warning})
	}
	return status
}
