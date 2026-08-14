package agentregistry

//go:generate go run ../../../../scripts/acp_registry_generate -output catalog_registry_generated.go

import (
	"slices"
	"sort"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// ConnectableAgent is one local ACP Agent shown by the guided /connect flow.
// RegistryID is non-empty when the launch metadata comes from the
// https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json
// generated snapshot.
type ConnectableAgent struct {
	ID          string
	DisplayName string
	Version     string
	Description string
	RegistryID  string
	// Priority orders maintained onboarding entries before the Registry's
	// stable source order. Zero preserves source order.
	Priority int
	// Preferred is the product-recommended member of Launchers.
	Preferred controlagents.LauncherChoice
	// Launchers is the complete declared launch-mode matrix for this entry.
	Launchers []controlagents.LauncherChoice
}

type registryNPXAgent struct {
	Agent   ConnectableAgent
	Package string
	Args    []string
	Env     map[string]string
}

type catalogPolicy struct {
	Priority   int
	Preferred  controlagents.LauncherChoice
	ManagedBin string
}

// catalogPolicies is the maintained Caelis product overlay on the generated
// upstream Registry snapshot. It owns display priority, preferred launch mode,
// and the two adapter packages that support verified managed/global installs.
var catalogPolicies = map[string]catalogPolicy{
	"codex":    {Priority: 10, Preferred: controlagents.LauncherChoiceManaged, ManagedBin: "codex-acp"},
	"claude":   {Priority: 20, Preferred: controlagents.LauncherChoiceManaged, ManagedBin: "claude-agent-acp"},
	"copilot":  {Priority: 40, Preferred: controlagents.LauncherChoiceNPX},
	"gemini":   {Priority: 50, Preferred: controlagents.LauncherChoiceNPX},
	"grok":     {Priority: 60, Preferred: controlagents.LauncherChoiceNPX},
	"opencode": {Priority: 70, Preferred: controlagents.LauncherChoiceInstalled},
}

var installedConnectableAgents = []struct {
	Agent  ConnectableAgent
	Config assembly.AgentConfig
}{
	{
		Agent: ConnectableAgent{
			ID: "opencode", DisplayName: "OpenCode", Version: "1.18.7", RegistryID: "opencode",
			Description: "The open source coding agent",
		},
		Config: nativeACPAgentConfig("opencode", "OpenCode ACP agent", "opencode", "acp"),
	},
	{
		Agent: ConnectableAgent{
			ID: "grok", DisplayName: "Grok Build", Version: "0.2.112", RegistryID: "grok-build",
			Description: "xAI's coding agent and CLI",
		},
		Config: nativeACPAgentConfig("grok", "Grok Build ACP agent", "grok", "agent", "stdio"),
	},
}

var customConnectableAgent = ConnectableAgent{
	ID:          "custom",
	DisplayName: "Custom command",
	Description: "Run another local ACP stdio command",
	Priority:    30,
	Preferred:   controlagents.LauncherChoiceCommand,
	Launchers:   []controlagents.LauncherChoice{controlagents.LauncherChoiceCommand},
}

// ConnectableAgents returns the detached local ACP onboarding catalog. Common
// Agents and the custom-command escape hatch use declared priorities; the rest
// preserve the official Registry order.
func ConnectableAgents() []ConnectableAgent {
	out := make([]ConnectableAgent, 0, len(registeredNPXAgents)+len(installedConnectableAgents)+1)
	seen := make(map[string]struct{}, cap(out))
	appendAgent := func(id string) {
		agent, ok := LookupConnectableAgent(id)
		if !ok {
			return
		}
		id = strings.ToLower(strings.TrimSpace(agent.ID))
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		out = append(out, agent)
		seen[id] = struct{}{}
	}
	for _, registered := range registeredNPXAgents {
		appendAgent(registered.Agent.ID)
	}
	for _, installed := range installedConnectableAgents {
		appendAgent(installed.Agent.ID)
	}
	appendAgent(customConnectableAgent.ID)
	sort.SliceStable(out, func(i, j int) bool {
		left := catalogPriority(out[i].Priority)
		right := catalogPriority(out[j].Priority)
		return left < right
	})
	return out
}

// LookupConnectableAgent resolves one catalog entry by its stable Caelis ID.
// RegistryID remains provenance only so one upstream Agent cannot be connected
// under two product identities.
func LookupConnectableAgent(name string) (ConnectableAgent, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == customConnectableAgent.ID {
		return cloneConnectableAgent(customConnectableAgent), true
	}
	var out ConnectableAgent
	found := false
	for _, registered := range registeredNPXAgents {
		if name == registered.Agent.ID {
			out = cloneConnectableAgent(registered.Agent)
			out.Launchers = appendLauncher(out.Launchers, controlagents.LauncherChoiceNPX)
			policy := catalogPolicies[name]
			out.Priority = firstPositive(policy.Priority, out.Priority)
			if policy.Preferred != "" {
				out.Preferred = policy.Preferred
			}
			if policy.ManagedBin != "" {
				out.Launchers = appendLauncher(out.Launchers, controlagents.LauncherChoiceManaged)
				out.Launchers = appendLauncher(out.Launchers, controlagents.LauncherChoiceGlobal)
			}
			if out.Preferred == "" {
				out.Preferred = controlagents.LauncherChoiceNPX
			}
			found = true
			break
		}
	}
	for _, installed := range installedConnectableAgents {
		if name == installed.Agent.ID {
			if !found {
				out = cloneConnectableAgent(installed.Agent)
			} else {
				out.Priority = firstPositive(out.Priority, installed.Agent.Priority)
			}
			policy := catalogPolicies[name]
			out.Priority = firstPositive(policy.Priority, out.Priority)
			if policy.Preferred != "" {
				out.Preferred = policy.Preferred
			}
			out.Launchers = appendLauncher(out.Launchers, controlagents.LauncherChoiceInstalled)
			if out.Preferred == "" {
				out.Preferred = controlagents.LauncherChoiceInstalled
			}
			found = true
			break
		}
	}
	if !found {
		return ConnectableAgent{}, false
	}
	return cloneConnectableAgent(out), true
}

// SupportsLauncher reports whether one catalog entry declared the requested
// launch mode.
func SupportsLauncher(name string, launcher controlagents.LauncherChoice) bool {
	agent, ok := LookupConnectableAgent(name)
	return ok && slices.Contains(agent.Launchers, launcher)
}

func managedBinFor(name string) string {
	return strings.TrimSpace(catalogPolicies[strings.ToLower(strings.TrimSpace(name))].ManagedBin)
}

// LookupNPXAgent returns the exact pinned npx launcher from the Registry
// snapshot.
func LookupNPXAgent(name string) (assembly.AgentConfig, bool) {
	registered, ok := lookupRegistryNPXAgent(name)
	if !ok {
		return assembly.AgentConfig{}, false
	}
	args := make([]string, 0, len(registered.Args)+2)
	args = append(args, "-y", registered.Package)
	args = append(args, registered.Args...)
	return assembly.AgentConfig{
		Name:        registered.Agent.ID,
		Description: registered.Agent.Description,
		Command:     "npx",
		Args:        args,
		Env:         cloneStringMap(registered.Env),
	}, true
}

// LookupInstalledAgent returns a detached installed-command launcher when
// Caelis maintains one for the catalog entry.
func LookupInstalledAgent(name string) (assembly.AgentConfig, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, installed := range installedConnectableAgents {
		if name != installed.Agent.ID {
			continue
		}
		out := installed.Config
		out.Args = append([]string(nil), out.Args...)
		out.Env = cloneStringMap(out.Env)
		return out, true
	}
	return assembly.AgentConfig{}, false
}

func lookupRegistryNPXAgent(name string) (registryNPXAgent, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, registered := range registeredNPXAgents {
		if name == registered.Agent.ID {
			registered.Args = append([]string(nil), registered.Args...)
			registered.Env = cloneStringMap(registered.Env)
			return registered, true
		}
	}
	return registryNPXAgent{}, false
}

func nativeACPAgentConfig(name string, description string, command string, args ...string) assembly.AgentConfig {
	return assembly.AgentConfig{
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		Command:     strings.TrimSpace(command),
		Args:        append([]string(nil), args...),
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

func appendLauncher(in []controlagents.LauncherChoice, launcher controlagents.LauncherChoice) []controlagents.LauncherChoice {
	if launcher == "" || slices.Contains(in, launcher) {
		return in
	}
	return append(in, launcher)
}

func catalogPriority(priority int) int {
	if priority > 0 {
		return priority
	}
	return int(^uint(0) >> 1)
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
