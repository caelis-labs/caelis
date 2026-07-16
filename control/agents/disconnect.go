package agents

import (
	"context"
	"fmt"
	"strings"
)

// DisconnectCandidate is one user-configured external ACP Agent that can be
// removed from the roster without affecting built-in, model-backed, system, or
// plugin-provided Agents.
type DisconnectCandidate struct {
	AgentID          string `json:"agent_id,omitempty"`
	Name             string `json:"name,omitempty"`
	ConnectionID     string `json:"connection_id,omitempty"`
	SiblingCount     int    `json:"sibling_count,omitempty"`
	LastOnConnection bool   `json:"last_on_connection,omitempty"`
}

// DisconnectResult describes the roster ownership released by one disconnect.
// Installed adapter files are outside this contract and are never removed.
type DisconnectResult struct {
	Agent             Agent  `json:"agent"`
	ConnectionID      string `json:"connection_id,omitempty"`
	ConnectionRemoved bool   `json:"connection_removed,omitempty"`
}

// AgentInUseError reports a recoverable task whose durable controller binding
// still depends on the Agent being disconnected.
type AgentInUseError struct {
	AgentID   string
	SessionID string
}

func (e *AgentInUseError) Error() string {
	if e == nil {
		return "control/agents: Agent is still in use"
	}
	return fmt.Sprintf(
		"control/agents: Agent %q controls recoverable task session %q; run /lead local in that task before disconnecting it",
		strings.TrimSpace(e.AgentID),
		strings.TrimSpace(e.SessionID),
	)
}

// Disconnector is the Control-owned local ACP Agent removal capability.
type Disconnector interface {
	DisconnectCandidates(context.Context) ([]DisconnectCandidate, error)
	DisconnectACP(context.Context, string) (DisconnectResult, error)
}

// ListDisconnectCandidates returns only persisted external ACP Agents. The
// result is deterministic and detached from the supplied roster.
func ListDisconnectCandidates(current Configuration) []DisconnectCandidate {
	current = NormalizeConfiguration(current)
	connectionReferences := make(map[string]int, len(current.Connections))
	for _, agent := range current.Agents {
		if agent.Backing.ConnectionID != "" {
			connectionReferences[agent.Backing.ConnectionID]++
		}
	}

	out := make([]DisconnectCandidate, 0, len(current.Agents))
	for _, agent := range current.Agents {
		connectionID := strings.TrimSpace(agent.Backing.ConnectionID)
		if connectionID == "" {
			continue
		}
		siblingCount := connectionReferences[connectionID] - 1
		out = append(out, DisconnectCandidate{
			AgentID:          agent.ID,
			Name:             agent.Name,
			ConnectionID:     connectionID,
			SiblingCount:     siblingCount,
			LastOnConnection: siblingCount == 0,
		})
	}
	return out
}

// DisconnectExternalAgent removes exactly one external ACP Agent. Its shared
// Connection remains while sibling Agents reference the endpoint. The removed
// Agent's model-scoped discoveries are released immediately; the final
// reference releases every remaining discovery and the Connection. Adapter
// installation is not represented by Configuration and is deliberately left
// untouched.
func DisconnectExternalAgent(current Configuration, agentID string) (Configuration, DisconnectResult, error) {
	current = NormalizeConfiguration(current)
	if err := ValidateConfiguration(current); err != nil {
		return Configuration{}, DisconnectResult{}, err
	}
	agentID = NormalizeName(agentID)
	if agentID == "" {
		return Configuration{}, DisconnectResult{}, fmt.Errorf("control/agents: Agent id is required")
	}
	removed, ok := LookupAgent(current, agentID)
	if !ok {
		return Configuration{}, DisconnectResult{}, fmt.Errorf("control/agents: Agent %q not found", agentID)
	}
	connectionID := strings.TrimSpace(removed.Backing.ConnectionID)
	if connectionID == "" {
		return Configuration{}, DisconnectResult{}, fmt.Errorf("control/agents: Agent %q is not backed by an external ACP connection", removed.ID)
	}

	next := Configuration{}
	connectionReferences := 0
	for _, agent := range current.Agents {
		if agent.ID == removed.ID {
			continue
		}
		next.Agents = append(next.Agents, agent)
		if agent.Backing.ConnectionID == connectionID {
			connectionReferences++
		}
	}
	connectionRemoved := connectionReferences == 0
	for _, connection := range current.Connections {
		if connectionRemoved && connection.ID == connectionID {
			continue
		}
		next.Connections = append(next.Connections, connection)
	}
	for _, snapshot := range current.Discoveries {
		if snapshot.ConnectionID == connectionID {
			if connectionRemoved || snapshot.SelectedModelID == removed.Defaults.ModelID {
				continue
			}
		}
		next.Discoveries = append(next.Discoveries, snapshot)
	}
	next = NormalizeConfiguration(next)
	if err := ValidateConfiguration(next); err != nil {
		return Configuration{}, DisconnectResult{}, err
	}
	return next, DisconnectResult{
		Agent:             removed,
		ConnectionID:      connectionID,
		ConnectionRemoved: connectionRemoved,
	}, nil
}
