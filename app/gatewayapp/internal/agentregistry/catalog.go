package agentregistry

import (
	"slices"
	"sort"
	"strings"

	controladapterhost "github.com/caelis-labs/caelis/control/adapterhost"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// ConnectableAgent is one local ACP Agent shown by the guided /connect flow.
// The catalog contains only native ACP commands maintained by Caelis and the
// generic custom-command entry; package installation remains user-owned.
type ConnectableAgent struct {
	ID          string
	DisplayName string
	Description string
	Priority    int
	Preferred   controlagents.LauncherChoice
	Launchers   []controlagents.LauncherChoice
}

type installedConnectableAgent struct {
	Agent          ConnectableAgent
	Config         assembly.AgentConfig
	CommandAliases []string
}

var installedConnectableAgents = []installedConnectableAgent{
	nativeACPAgent("grok", "Grok Build", "xAI's coding agent with a native ACP stdio command", 10, "grok", "agent", "stdio"),
	nativeACPAgent("kimi", "Kimi CLI", "Kimi coding agent with a native ACP stdio command", 20, "kimi", "acp"),
	nativeACPAgent("opencode", "OpenCode", "Open source coding agent with a native ACP stdio command", 30, "opencode", "acp"),
	nativeACPAgent("copilot", "GitHub Copilot", "GitHub's coding agent with a native ACP stdio mode", 40, "copilot", "--acp"),
	nativeACPAgentWithAliases(
		"qoder", "Qoder CLI", "Qoder coding agent with a native ACP stdio mode", 50,
		"qoder", []string{"qodercli"}, "--acp",
	),
	nativeACPAgent("gemini", "Gemini CLI", "Google's Gemini coding agent with a native ACP stdio mode", 60, "gemini", "--acp"),
	nativeACPAgent("qwen-code", "Qwen Code", "Alibaba's Qwen coding agent with a native ACP stdio mode", 70, "qwen", "--acp"),
	nativeACPAgentWithEnv(
		"auggie", "Auggie CLI", "Augment Code's coding agent with a native ACP stdio mode", 80,
		"auggie", map[string]string{"AUGMENT_DISABLE_AUTO_UPDATE": "1"}, "--acp",
	),
	nativeACPAgent("cline", "Cline", "Cline coding agent with a native ACP stdio mode", 90, "cline", "--acp"),
	nativeACPAgentWithEnv(
		"factory-droid", "Factory Droid", "Factory's coding agent with a native ACP stdio mode", 100,
		"droid", map[string]string{
			"DROID_DISABLE_AUTO_UPDATE":         "true",
			"FACTORY_DROID_AUTO_UPDATE_ENABLED": "false",
		}, "exec", "--output-format", "acp-daemon",
	),
	nativeACPAgent("goose", "Goose", "Block's open source coding agent with a native ACP stdio command", 110, "goose", "acp"),
	nativeACPAgent("kilo", "Kilo", "Kilo coding agent with a native ACP stdio command", 120, "kilo", "acp"),
}

var hostedConnectableAgents = []ConnectableAgent{{
	ID:          controladapterhost.CodexAdapterID,
	DisplayName: "Codex CLI (built in)",
	Description: "Codex CLI through the Caelis built-in app-server adapter",
	Priority:    5,
	Preferred:   controlagents.LauncherChoiceHosted,
	Launchers:   []controlagents.LauncherChoice{controlagents.LauncherChoiceHosted},
}}

var customConnectableAgent = ConnectableAgent{
	ID:          "custom",
	DisplayName: "Custom command",
	Description: "Run an ACP stdio command you installed and made available on PATH",
	Priority:    1000,
	Preferred:   controlagents.LauncherChoiceCommand,
	Launchers:   []controlagents.LauncherChoice{controlagents.LauncherChoiceCommand},
}

// ConnectableAgents returns the detached local ACP onboarding catalog.
func ConnectableAgents() []ConnectableAgent {
	out := make([]ConnectableAgent, 0, len(hostedConnectableAgents)+len(installedConnectableAgents)+1)
	for _, hosted := range hostedConnectableAgents {
		out = append(out, cloneConnectableAgent(hosted))
	}
	for _, installed := range installedConnectableAgents {
		agent, ok := LookupConnectableAgent(installed.Agent.ID)
		if ok {
			out = append(out, agent)
		}
	}
	out = append(out, cloneConnectableAgent(customConnectableAgent))
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Priority < out[j].Priority
	})
	return out
}

// LookupConnectableAgent resolves one catalog entry by its stable Caelis ID.
func LookupConnectableAgent(name string) (ConnectableAgent, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == customConnectableAgent.ID {
		return cloneConnectableAgent(customConnectableAgent), true
	}
	for _, hosted := range hostedConnectableAgents {
		if name == hosted.ID {
			return cloneConnectableAgent(hosted), true
		}
	}
	for _, installed := range installedConnectableAgents {
		if name != installed.Agent.ID {
			continue
		}
		out := cloneConnectableAgent(installed.Agent)
		out.Preferred = controlagents.LauncherChoiceInstalled
		out.Launchers = []controlagents.LauncherChoice{controlagents.LauncherChoiceInstalled}
		return out, true
	}
	return ConnectableAgent{}, false
}

// SupportsLauncher reports whether one catalog entry declared the requested
// launch mode.
func SupportsLauncher(name string, launcher controlagents.LauncherChoice) bool {
	agent, ok := LookupConnectableAgent(name)
	return ok && slices.Contains(agent.Launchers, launcher)
}

// LookupInstalledAgent returns a detached native-command launcher maintained
// for the catalog entry. The command itself must be supplied by the user
// environment and visible on PATH.
func LookupInstalledAgent(name string) (assembly.AgentConfig, bool) {
	installed, ok := lookupInstalledConnectableAgent(name)
	if !ok {
		return assembly.AgentConfig{}, false
	}
	out := installed.Config
	out.Args = append([]string(nil), out.Args...)
	out.Env = cloneStringMap(out.Env)
	return out, true
}

// InstalledAgentCommandCandidates returns the detached ordered PATH command
// names accepted for one native ACP preset. The first value is canonical;
// later values preserve official installer naming transitions.
func InstalledAgentCommandCandidates(name string) []string {
	installed, ok := lookupInstalledConnectableAgent(name)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(installed.CommandAliases)+1)
	out = append(out, installed.Config.Command)
	out = append(out, installed.CommandAliases...)
	return out
}

func lookupInstalledConnectableAgent(name string) (installedConnectableAgent, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, installed := range installedConnectableAgents {
		if name != installed.Agent.ID {
			continue
		}
		installed.CommandAliases = append([]string(nil), installed.CommandAliases...)
		return installed, true
	}
	return installedConnectableAgent{}, false
}

func nativeACPAgentConfig(name string, description string, command string, args ...string) assembly.AgentConfig {
	return assembly.AgentConfig{
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		Command:     strings.TrimSpace(command),
		Args:        append([]string(nil), args...),
	}
}

func nativeACPAgent(
	id string,
	displayName string,
	description string,
	priority int,
	command string,
	args ...string,
) installedConnectableAgent {
	return nativeACPAgentSpec(id, displayName, description, priority, command, nil, nil, args...)
}

func nativeACPAgentWithEnv(
	id string,
	displayName string,
	description string,
	priority int,
	command string,
	env map[string]string,
	args ...string,
) installedConnectableAgent {
	return nativeACPAgentSpec(id, displayName, description, priority, command, nil, env, args...)
}

func nativeACPAgentWithAliases(
	id string,
	displayName string,
	description string,
	priority int,
	command string,
	commandAliases []string,
	args ...string,
) installedConnectableAgent {
	return nativeACPAgentSpec(id, displayName, description, priority, command, commandAliases, nil, args...)
}

func nativeACPAgentSpec(
	id string,
	displayName string,
	description string,
	priority int,
	command string,
	commandAliases []string,
	env map[string]string,
	args ...string,
) installedConnectableAgent {
	config := nativeACPAgentConfig(id, description, command, args...)
	config.Env = cloneStringMap(env)
	return installedConnectableAgent{
		Agent: ConnectableAgent{
			ID: strings.TrimSpace(id), DisplayName: strings.TrimSpace(displayName),
			Description: strings.TrimSpace(description), Priority: priority,
		},
		Config:         config,
		CommandAliases: append([]string(nil), commandAliases...),
	}
}

func cloneConnectableAgent(in ConnectableAgent) ConnectableAgent {
	out := in
	out.ID = strings.ToLower(strings.TrimSpace(in.ID))
	out.DisplayName = strings.TrimSpace(in.DisplayName)
	out.Launchers = append([]controlagents.LauncherChoice(nil), in.Launchers...)
	if preferred := slices.Index(out.Launchers, out.Preferred); preferred > 0 {
		out.Launchers[0], out.Launchers[preferred] = out.Launchers[preferred], out.Launchers[0]
	}
	return out
}
