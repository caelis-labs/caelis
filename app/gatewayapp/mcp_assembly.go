package gatewayapp

import (
	"context"
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
	for i := range specs {
		if specs[i].PluginID == mcpconfig.NamespaceProject {
			specs[i].ConnectionScope = workspaceDir
		}
	}
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

// A serving Host routes execution to Session Runtimes. Its process-default
// composition has no MCP view. Direct embedded callers can still execute with
// the process-default Runtime and retain its configured startup selection.
func (s *runtimeComposition) configureNativeMCP(ctx context.Context, configured, plugins []sdkmcp.ServerSpec) (*sdkmcp.Manager, error) {
	var startupNames map[string]bool
	if s.process != nil && s.process.sessionScopedMCP {
		return nil, nil
	}
	if s.activation == nil {
		startupNames = make(map[string]bool, len(configured))
		for _, spec := range configured {
			startupNames[spec.Name] = true
		}
	}
	return s.configureMCP(ctx, nil, func(current []sdkmcp.ServerSpec) ([]sdkmcp.ServerSpec, error) {
		if startupNames != nil {
			current = slices.DeleteFunc(slices.Clone(current), func(spec sdkmcp.ServerSpec) bool {
				return !startupNames[spec.Name]
			})
		}
		return mergeRuntimeMCPSpecs(current, plugins)
	}, configured, s.mcpFailureNotifier())
}

func (s *runtimeComposition) configureMCP(ctx context.Context, manager *sdkmcp.Manager, compose func([]sdkmcp.ServerSpec) ([]sdkmcp.ServerSpec, error), configured []sdkmcp.ServerSpec, onFailure func(sdkmcp.ServerFailure)) (*sdkmcp.Manager, error) {
	if host := s.authorities.mcp; host != nil {
		return host.configure(ctx, manager, hostMCPView{workspace: s.workspace.CWD, compose: compose}, onFailure)
	}
	specs, err := compose(configured)
	if err != nil {
		return nil, err
	}
	if manager != nil {
		return manager, manager.Reconcile(specs)
	}
	return sdkmcp.NewManager(context.WithoutCancel(ctx), specs, onFailure)
}

func (s *runtimeComposition) detachMCP(manager *sdkmcp.Manager) {
	if host := s.authorities.mcp; host != nil && manager != nil {
		host.detach(manager)
	}
}
