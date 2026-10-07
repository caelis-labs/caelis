package gatewayapp

import (
	"context"
	"fmt"
	"os"
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
	manager *mcp.Manager
	store   *application.Store
	scope   application.Scope
}

func (s applicationMCPSource) Tools() []tool.Tool {
	ready := s.manager.Tools()
	for i, item := range ready {
		ready[i] = applicationLeasedTool{Tool: item, store: s.store, scope: s.scope}
	}
	return ready
}

func applicationSkillCatalog(profile application.Profile) (skill.Catalog, error) {
	var metas []skill.Meta
	if len(profile.SkillDirs) != 0 {
		for _, dir := range profile.SkillDirs {
			info, err := os.Stat(dir)
			if err != nil {
				return skill.Catalog{}, fmt.Errorf("application Skill directory %q: %w", dir, err)
			}
			if !info.IsDir() {
				return skill.Catalog{}, fmt.Errorf("application Skill directory %q is not a directory", dir)
			}
		}
		found, err := skillfs.DiscoverMetaRequest(skill.DiscoverRequest{Dirs: profile.SkillDirs})
		if err != nil {
			return skill.Catalog{}, err
		}
		metas = append(metas, found...)
	}
	for _, root := range profile.SkillRoots {
		meta, err := skillfs.DiscoverRootMeta(root)
		if err != nil {
			return skill.Catalog{}, fmt.Errorf("application Skill root %q: %w", root, err)
		}
		metas = append(metas, meta)
	}
	seen := make(map[string]bool, len(metas))
	for _, meta := range metas {
		name := strings.ToLower(meta.Name)
		if seen[name] {
			return skill.Catalog{}, fmt.Errorf("application Skill name %q is duplicated", meta.Name)
		}
		seen[name] = true
	}
	return skill.NewCatalog(metas), nil
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
	catalog, err := applicationSkillCatalog(configuration.Profile)
	if err != nil {
		return nil, nil, err
	}
	assembled := &applicationCapabilities{catalog: catalog}
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
		assembled.source = applicationMCPSource{manager: resource.manager, store: r.composition.authorities.applications, scope: r.binding.Scope}
		assembled.searchTool = toolsearch.NewSource(assembled.source)
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

func (r *applicationTurnResolver) capabilityStatus(revision uint64) []application.MCPServerStatus {
	r.capabilitiesMu.Lock()
	capability := r.capabilities[revision]
	current := r.currentMCP
	desired := r.desiredRevision
	r.capabilitiesMu.Unlock()
	if capability != nil {
		current = capability.resource
	} else if revision != desired {
		current = nil
	}
	if current == nil {
		return nil
	}
	infos := current.manager.GetServerInfos("application")
	result := make([]application.MCPServerStatus, 0, len(infos))
	for _, info := range infos {
		result = append(result, application.MCPServerStatus{Name: info.Name, Status: info.Status, Tools: info.Tools, Warning: info.Warning})
	}
	return result
}
