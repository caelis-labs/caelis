// Package agents contains app-level ACP agent catalog definitions.
package agents

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/core/plugin"
)

const claudeACPAdapterVersion = "^0.31.0"

func BuiltinACPAgents() []plugin.ACPAgentDescriptor {
	return []plugin.ACPAgentDescriptor{
		npxAgent("codex", "OpenAI Codex ACP agent", "@zed-industries/codex-acp"),
		npxAgent("claude", "Claude Code ACP agent", "@agentclientprotocol/claude-agent-acp@"+claudeACPAdapterVersion),
		nativeAgent("opencode", "OpenCode ACP agent", "opencode", "acp"),
		nativeAgent("codefree-o", "CodeFree-O ACP agent", "codefree-o", "acp"),
		nativeAgent("copilot", "GitHub Copilot ACP agent", "copilot", "--acp", "--stdio"),
		nativeAgent("gemini", "Gemini ACP agent", "gemini", "--acp"),
	}
}

func LookupBuiltinACPAgent(name string) (plugin.ACPAgentDescriptor, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, agent := range BuiltinACPAgents() {
		if strings.EqualFold(strings.TrimSpace(agent.Name), name) {
			return cloneACPAgent(agent), true
		}
	}
	return plugin.ACPAgentDescriptor{}, false
}

func nativeAgent(name string, description string, command string, args ...string) plugin.ACPAgentDescriptor {
	return plugin.ACPAgentDescriptor{
		Name:        strings.ToLower(strings.TrimSpace(name)),
		Description: strings.TrimSpace(description),
		Command:     strings.TrimSpace(command),
		Args:        append([]string(nil), args...),
		Roles:       []string{"participant"},
	}
}

func npxAgent(name string, description string, pkg string) plugin.ACPAgentDescriptor {
	return plugin.ACPAgentDescriptor{
		Name:        strings.ToLower(strings.TrimSpace(name)),
		Description: strings.TrimSpace(description),
		Command:     "npx",
		Args:        []string{"-y", strings.TrimSpace(pkg)},
		Roles:       []string{"participant"},
	}
}

func cloneACPAgent(agent plugin.ACPAgentDescriptor) plugin.ACPAgentDescriptor {
	agent.Args = append([]string(nil), agent.Args...)
	agent.Env = cloneStringMap(agent.Env)
	agent.Roles = append([]string(nil), agent.Roles...)
	return agent
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
