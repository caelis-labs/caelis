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
	for _, spec := range combined {
		if spec.PluginID == "desktop-world" {
			continue
		}
		projected := sdkmcp.ServerToolNamePrefix(spec.Name)
		// Keep the historical namespace reserved while released packages can
		// still be selected. A rename must not transfer old tool names to a
		// different source.
		for _, name := range []string{"dtw", "desktop-world"} {
			reserved := sdkmcp.ServerToolNamePrefix(name)
			if strings.HasPrefix(projected, reserved) || strings.HasPrefix(reserved, projected) {
				return nil, fmt.Errorf("MCP source %q conflicts with Core's fixed %s namespace", spec.Name, name)
			}
		}
	}
	return combined, nil
}
