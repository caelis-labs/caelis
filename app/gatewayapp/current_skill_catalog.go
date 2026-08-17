package gatewayapp

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
)

// CurrentSkillCatalog discovers the configured skills for a future Session
// without assembling an execution Runtime. In particular, completion must not
// start plugin MCP servers, hooks, sandboxes, or controllers merely to read
// Skill metadata.
func (s WorkspaceReadService) CurrentSkillCatalog(ctx context.Context, workspace session.WorkspaceRef) (skill.Catalog, error) {
	if s.composition == nil || s.composition.authorities.store == nil {
		return skill.Catalog{}, errors.New("gatewayapp: App configuration store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return skill.Catalog{}, err
	}
	doc, err := s.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		return skill.Catalog{}, err
	}
	resolved, err := s.Resolve(workspace)
	if err != nil {
		return skill.Catalog{}, err
	}
	workspaceDir := strings.TrimSpace(resolved.CWD)
	s.composition.mu.RLock()
	skillDirs := stackSkillDiscoveryDirs(workspaceDir, s.composition.runtime.SkillDirs)
	s.composition.mu.RUnlock()
	contributions, err := resolveGatewayPluginContributions(doc.Plugins)
	if err != nil {
		return skill.Catalog{}, err
	}
	metas, err := DiscoverSkillMetaRequest(skill.DiscoverRequest{
		Dirs:          skillDirs,
		WorkspaceDir:  workspaceDir,
		PluginBundles: skill.ClonePluginBundles(contributions.SkillBundles),
	})
	if err != nil {
		return skill.Catalog{}, err
	}
	if err := ctx.Err(); err != nil {
		return skill.Catalog{}, err
	}
	sort.Slice(metas, func(i, j int) bool {
		return strings.TrimSpace(metas[i].Path) < strings.TrimSpace(metas[j].Path)
	})
	return skill.NewCatalog(metas), nil
}
