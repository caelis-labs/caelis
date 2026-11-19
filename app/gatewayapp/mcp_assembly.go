package gatewayapp

import (
	"fmt"
	"slices"
	"strings"

	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	sdkmcp "github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/mcpconfig"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/workspacetrust"
)

func resolveConfiguredMCPServers(native mcpconfig.Servers, trust workspacetrust.Configuration, workspaceDir string) ([]sdkmcp.ServerSpec, error) {
	workspaceDir = strings.TrimSpace(workspaceDir)
	userAgentsFile, projectAgentsFile, projectMCPFile := mcpConfigFiles(workspaceDir)
	specs, err := mcpconfig.ResolveAvailable(mcpconfig.Request{
		Native:               native,
		UserAgentsFile:       userAgentsFile,
		ProjectAgentsFile:    projectAgentsFile,
		ProjectMCPFile:       projectMCPFile,
		ProjectRoot:          workspaceDir,
		AllowProjectOverlays: workspacetrust.Lookup(trust, workspaceDir) == workspacetrust.Trusted,
	})
	return slices.DeleteFunc(specs, func(spec sdkmcp.ServerSpec) bool {
		return reservedMCPNamespace(spec) != ""
	}), err
}

func mcpConfigFiles(workspaceDir string) (user, projectAgents, projectMCP string) {
	user, _ = skillfs.ResolvePath("~/.agents/mcp.json")
	projectAgents, projectMCP = mcpconfig.ProjectFiles(workspaceDir)
	return user, projectAgents, projectMCP
}

func mergeRuntimeMCPSpecs(catalog []sdkmcp.ServerSpec, pluginSpecs []plugin.MCPServerSpec) ([]sdkmcp.ServerSpec, error) {
	combined, err := mcpconfig.CombineSpecs(catalog, pluginSpecs)
	if err != nil {
		return nil, err
	}
	for _, spec := range combined {
		if name := reservedMCPNamespace(spec); name != "" {
			return nil, fmt.Errorf("MCP source %q conflicts with Core's fixed %s namespace", spec.Name, name)
		}
	}
	return combined, nil
}

func reservedMCPNamespace(spec sdkmcp.ServerSpec) string {
	if spec.PluginID == "desktop-world" {
		return ""
	}
	projected := sdkmcp.ServerToolNamePrefix(spec.Name)
	// Keep the historical namespace reserved while released packages can
	// still be selected.
	for _, name := range []string{"dtw", "desktop-world"} {
		reserved := sdkmcp.ServerToolNamePrefix(name)
		if strings.HasPrefix(projected, reserved) || strings.HasPrefix(reserved, projected) {
			return name
		}
	}
	return ""
}
