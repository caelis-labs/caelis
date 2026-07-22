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

// ResolveContributions parses configured plugins and projects the contributions
// consumed by Runtime assembly. Broken disabled plugins are ignored; a broken
// enabled plugin makes the configuration invalid.
func ResolveContributions(configs []Config) (Contributions, error) {
	var out Contributions
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
		out.MCPServerSpecs = append(out.MCPServerSpecs, installed.MCPServers...)
		for _, contributed := range installed.Agents {
			out.Agents = append(out.Agents, AgentRegistration{
				PluginID: installed.ID,
				Agent:    contributed,
			})
		}
	}
	return out, nil
}
