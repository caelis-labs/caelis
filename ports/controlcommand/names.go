package controlcommand

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/control"
)

// AgentNameFilter decides whether a normalized registered-agent slash command
// name is eligible for a surface.
type AgentNameFilter func(string) bool

// AgentLister is the narrow control.Service subset needed to discover
// registered agent slash commands.
type AgentLister interface {
	ListAgents(context.Context, int) ([]control.AgentCandidate, error)
}

// AppendRegisteredAgentNames appends registered agent command names to base
// while preserving base order and removing duplicates case-insensitively.
func AppendRegisteredAgentNames(ctx context.Context, service AgentLister, base []string, filters ...AgentNameFilter) []string {
	out := append([]string(nil), base...)
	if service == nil {
		return out
	}
	if ctx == nil {
		ctx = context.Background()
	}
	agents, err := service.ListAgents(ctx, 200)
	if err != nil {
		return out
	}
	names := make([]string, 0, len(agents))
	for _, agent := range agents {
		names = append(names, agent.Name)
	}
	return AppendAgentNames(out, names, filters...)
}

// AppendAgentNames appends normalized agent command names to base while
// preserving base order and removing duplicates case-insensitively.
func AppendAgentNames(base []string, names []string, filters ...AgentNameFilter) []string {
	out := append([]string(nil), base...)
	seen := map[string]struct{}{}
	for _, command := range out {
		seen[strings.ToLower(strings.TrimSpace(command))] = struct{}{}
	}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || !agentNameAllowed(name, filters...) {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		out = append(out, name)
		seen[name] = struct{}{}
	}
	return out
}

// RegisteredAgentNameAllowed reports whether command names a registered agent
// that is accepted by the provided surface filters.
func RegisteredAgentNameAllowed(ctx context.Context, service AgentLister, command string, filters ...AgentNameFilter) bool {
	if service == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	agents, err := service.ListAgents(ctx, 200)
	if err != nil {
		return false
	}
	names := make([]string, 0, len(agents))
	for _, agent := range agents {
		names = append(names, agent.Name)
	}
	return AgentNameAllowed(names, command, filters...)
}

// AgentNameAllowed reports whether command appears in names and passes all
// surface filters.
func AgentNameAllowed(names []string, command string, filters ...AgentNameFilter) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" || !agentNameAllowed(command, filters...) {
		return false
	}
	for _, name := range names {
		if strings.EqualFold(strings.TrimSpace(name), command) {
			return true
		}
	}
	return false
}

func agentNameAllowed(name string, filters ...AgentNameFilter) bool {
	for _, filter := range filters {
		if filter != nil && !filter(name) {
			return false
		}
	}
	return true
}
