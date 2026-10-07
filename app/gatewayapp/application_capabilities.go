package gatewayapp

import (
	"context"
	"fmt"
	"os"
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
	manager    *mcp.Manager
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

func (r *applicationTurnResolver) capabilitiesFor(ctx context.Context, configuration application.Configuration) (*applicationCapabilities, error) {
	r.capabilitiesMu.Lock()
	defer r.capabilitiesMu.Unlock()
	if existing := r.capabilities[configuration.Revision]; existing != nil {
		return existing, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	catalog, err := applicationSkillCatalog(configuration.Profile)
	if err != nil {
		return nil, err
	}
	assembled := &applicationCapabilities{catalog: catalog}
	if len(catalog.Metas()) != 0 {
		assembled.skillTool = skilltool.New(skilltool.Config{Loader: skillfs.Loader{}, Catalog: catalog})
	}
	if len(configuration.Profile.MCPServers) != 0 {
		manager, err := mcp.NewManager(context.WithoutCancel(ctx), applicationMCPSpecs(configuration.Profile), nil)
		if err != nil {
			return nil, err
		}
		assembled.manager = manager
		assembled.source = applicationMCPSource{manager: manager, store: r.composition.authorities.applications, scope: r.binding.Scope}
		assembled.searchTool = toolsearch.NewSource(assembled.source)
	}
	if r.capabilities == nil {
		r.capabilities = map[uint64]*applicationCapabilities{}
	}
	r.capabilities[configuration.Revision] = assembled
	return assembled, nil
}

func (r *applicationTurnResolver) closeCapabilities() {
	r.capabilitiesMu.Lock()
	assembled := r.capabilities
	r.capabilities = nil
	r.capabilitiesMu.Unlock()
	for _, capability := range assembled {
		if capability.manager != nil {
			_ = capability.manager.Close()
		}
	}
}

func (r *applicationTurnResolver) capabilityStatus(revision uint64) []application.MCPServerStatus {
	r.capabilitiesMu.Lock()
	capability := r.capabilities[revision]
	r.capabilitiesMu.Unlock()
	if capability == nil || capability.manager == nil {
		return nil
	}
	infos := capability.manager.GetServerInfos("application")
	result := make([]application.MCPServerStatus, 0, len(infos))
	for _, info := range infos {
		result = append(result, application.MCPServerStatus{Name: info.Name, Status: info.Status, Tools: info.Tools, Warning: info.Warning})
	}
	return result
}
