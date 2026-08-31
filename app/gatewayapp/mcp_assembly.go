package gatewayapp

import (
	"strings"

	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	sdkmcp "github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/mcpconfig"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/workspacetrust"
)

func resolveConfiguredMCPServers(native mcpconfig.Servers, trust workspacetrust.Configuration, workspaceDir string) ([]sdkmcp.ServerSpec, error) {
	userAgentsFile := ""
	if resolved, err := skillfs.ResolvePath("~/.agents/mcp.json"); err == nil {
		userAgentsFile = resolved
	}
	workspaceDir = strings.TrimSpace(workspaceDir)
	projectAgentsFile, projectMCPFile := mcpconfig.ProjectFiles(workspaceDir)
	return mcpconfig.Resolve(mcpconfig.Request{
		Native:               native,
		UserAgentsFile:       userAgentsFile,
		ProjectAgentsFile:    projectAgentsFile,
		ProjectMCPFile:       projectMCPFile,
		ProjectRoot:          workspaceDir,
		AllowProjectOverlays: workspacetrust.Lookup(trust, workspaceDir) == workspacetrust.Trusted,
	})
}

func mergeRuntimeMCPSpecs(catalog []sdkmcp.ServerSpec, pluginSpecs []plugin.MCPServerSpec) ([]sdkmcp.ServerSpec, error) {
	return mcpconfig.CombineSpecs(catalog, pluginSpecs)
}
