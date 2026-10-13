package plugin

import (
	"fmt"
	"runtime"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
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
	// TargetOS defaults to the current OS; an explicit value permits portable
	// verification of process environment semantics during assembly.
	TargetOS string
}

// ResolveContributions strictly validates configured plugins for explicit
// configuration changes. Broken enabled plugins return an actionable error.
func ResolveContributions(configs []Config, paths ...RuntimePaths) (Contributions, error) {
	return resolveContributions(configs, false, paths...)
}

// ResolveAvailableContributions assembles persisted optional plugin content.
// A broken package or MCP server cannot prevent unrelated Runtime inputs from
// loading. Explicit installation and configuration still use ResolveContributions.
func ResolveAvailableContributions(configs []Config, paths ...RuntimePaths) (Contributions, error) {
	return resolveContributions(configs, true, paths...)
}

func resolveContributions(configs []Config, skipBroken bool, paths ...RuntimePaths) (Contributions, error) {
	var out Contributions
	var runtimePaths RuntimePaths
	if len(paths) > 0 {
		runtimePaths = paths[0]
	}
	if runtimePaths.TargetOS == "" {
		runtimePaths.TargetOS = runtime.GOOS
	}
	for _, configured := range configs {
		parse := ParseConfigured
		if skipBroken {
			parse = ParseConfiguredAvailable
		}
		installed, err := parse(configured)
		if err != nil {
			if configured.Enabled && !skipBroken {
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
					if skipBroken {
						continue
					}
					return out, fmt.Errorf("plugin %q MCP server %q: %w", installed.ID, spec.Name, err)
				}
				spec, err = applyHostExecutableEnv(spec, configured.ExecutableEnv, runtimePaths.TargetOS)
				if err != nil {
					if skipBroken {
						continue
					}
					return out, fmt.Errorf("plugin %q MCP server %q: %w", installed.ID, spec.Name, err)
				}
			} else {
				var err error
				spec, err = instantiateClaudeProjectDir(spec, runtimePaths.WorkspaceDir)
				if err != nil {
					if skipBroken {
						continue
					}
					return out, fmt.Errorf("plugin %q MCP server %q: %w", installed.ID, spec.Name, err)
				}
			}
			if err := mcp.ValidateServerIdentity(spec); err != nil {
				if skipBroken {
					continue
				}
				return out, fmt.Errorf("plugin %q MCP server %q: %w", installed.ID, spec.Name, err)
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
