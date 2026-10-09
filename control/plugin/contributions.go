package plugin

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
)

// Contributions is the normalized set of Runtime inputs produced by the
// current Plugin configuration.
type Contributions struct {
	SkillBundles      []skill.PluginBundle
	SessionStartHooks []HookSpec
	MCPServerSpecs    []MCPServerSpec
	Agents            []AgentRegistration
}

// AgentRegistration associates a contributed Agent with its owning plugin.
type AgentRegistration struct {
	PluginID string
	Agent    AgentContribution
}

// RuntimePaths are host-owned paths used only when materializing plugin MCP
// servers for one Session snapshot. Listing Skills does not create data dirs.
type RuntimePaths struct {
	StoreDir     string
	WorkspaceDir string
}

// ResolveContributions parses configured plugins and projects the contributions
// consumed by Runtime assembly. Broken disabled plugins are ignored; a broken
// enabled plugin makes the configuration invalid.
func ResolveContributions(configs []Config, paths ...RuntimePaths) (Contributions, error) {
	var out Contributions
	var runtimePaths RuntimePaths
	if len(paths) > 0 {
		runtimePaths = paths[0]
	}
	for _, configured := range configs {
		installed, err := ParseConfigured(configured)
		if err != nil {
			if configured.Enabled {
				return out, fmt.Errorf("parse enabled plugin %q failed: %w", configured.ID, err)
			}
			continue
		}
		out.SkillBundles = append(out.SkillBundles, pluginSkillBundles(installed, configured.Enabled)...)
		if !configured.Enabled {
			continue
		}
		for _, hook := range installed.Hooks {
			if hook.Event == HookEventSessionStart {
				out.SessionStartHooks = append(out.SessionStartHooks, hook)
			}
		}
		for _, spec := range installed.MCPServers {
			if installed.Kind == ManifestKindAgentPlugin {
				dataDir, err := agentPluginDataDir(runtimePaths.StoreDir, installed.ID)
				if err != nil {
					return out, fmt.Errorf("plugin %q: %w", installed.ID, err)
				}
				spec, err = instantiateAgentPluginMCP(spec, installed.Root, dataDir)
				if err != nil {
					return out, fmt.Errorf("plugin %q MCP server %q: %w", installed.ID, spec.Name, err)
				}
				spec, err = applyHostExecutableEnv(spec, configured.ExecutableEnv)
				if err != nil {
					return out, fmt.Errorf("plugin %q MCP server %q: %w", installed.ID, spec.Name, err)
				}
			} else {
				var err error
				spec, err = instantiateClaudeProjectDir(spec, runtimePaths.WorkspaceDir)
				if err != nil {
					return out, fmt.Errorf("plugin %q MCP server %q: %w", installed.ID, spec.Name, err)
				}
			}
			out.MCPServerSpecs = append(out.MCPServerSpecs, spec)
		}
		for _, contributed := range installed.Agents {
			out.Agents = append(out.Agents, AgentRegistration{
				PluginID: installed.ID,
				Agent:    contributed,
			})
		}
	}
	return out, nil
}
