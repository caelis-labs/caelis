package gatewayapp

import (
	"fmt"
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
	return mcpconfig.ResolveAvailable(mcpconfig.Request{
		Native:               native,
		UserAgentsFile:       userAgentsFile,
		ProjectAgentsFile:    projectAgentsFile,
		ProjectMCPFile:       projectMCPFile,
		ProjectRoot:          workspaceDir,
		AllowProjectOverlays: workspacetrust.Lookup(trust, workspaceDir) == workspacetrust.Trusted,
	})
}

func mergeRuntimeMCPSpecs(catalog []sdkmcp.ServerSpec, pluginSpecs []plugin.MCPServerSpec) ([]sdkmcp.ServerSpec, error) {
	combined, err := mcpconfig.CombineSpecs(catalog, pluginSpecs)
	if err != nil {
		return nil, err
	}
	reserved := sdkmcp.ServerToolNamePrefix("desktop-world")
	for _, spec := range combined {
		projected := sdkmcp.ServerToolNamePrefix(spec.Name)
		if spec.PluginID != "desktop-world" && (strings.HasPrefix(projected, reserved) || strings.HasPrefix(reserved, projected)) {
			return nil, fmt.Errorf("MCP source %q conflicts with Core's fixed desktop-world namespace", spec.Name)
		}
	}
	return combined, nil
}
