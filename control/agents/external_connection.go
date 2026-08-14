package agents

import (
	"fmt"
	"strings"
)

// UpsertExternalConnection persists one stable external Agent identity for an
// ACP connection and one model-scoped discovery snapshot. Model selection and
// defaults belong to ModelProfiles, not to the Agent.
func UpsertExternalConnection(
	current Configuration,
	connection Connection,
	snapshot DiscoverySnapshot,
	allowed NameFilter,
) (Configuration, Agent, error) {
	current = NormalizeConfiguration(current)
	connection = NormalizeConnection(connection)
	if err := ValidateConnection(connection); err != nil {
		return Configuration{}, Agent{}, err
	}

	next := Configuration{}
	used := make(map[string]struct{}, len(current.Agents))
	var existing Agent
	for _, agent := range current.Agents {
		if agent.ConnectionID == connection.ID {
			if existing.ID != "" {
				return Configuration{}, Agent{}, fmt.Errorf(
					"control/agents: connection %q has more than one Agent identity",
					connection.ID,
				)
			}
			existing = agent
			continue
		}
		next.Agents = append(next.Agents, agent)
		used[agent.ID] = struct{}{}
	}

	id := existing.ID
	if !agentIDAvailable(id, used, allowed) {
		id = allocateAgentID(
			firstNonEmpty(connection.Name, connection.ID),
			"agent",
			"connection\x00"+connection.ID,
			used,
			allowed,
		)
	}
	if id == "" {
		return Configuration{}, Agent{}, fmt.Errorf(
			"control/agents: cannot allocate an Agent id for connection %q",
			connection.ID,
		)
	}
	agent := NormalizeAgent(Agent{
		ID:           id,
		Name:         firstNonEmpty(connection.Name, connection.ID),
		ConnectionID: connection.ID,
	})
	next.Agents = append(next.Agents, agent)

	for _, item := range current.Connections {
		if item.ID != connection.ID {
			next.Connections = append(next.Connections, item)
		}
	}
	next.Connections = append(next.Connections, connection)

	snapshot = NormalizeDiscoverySnapshot(snapshot)
	snapshot.ConnectionID = connection.ID
	snapshot.LaunchFingerprint = LaunchFingerprint(connection.Launcher)
	for _, item := range current.Discoveries {
		if item.ConnectionID != connection.ID {
			next.Discoveries = append(next.Discoveries, item)
			continue
		}
		if item.LaunchFingerprint != snapshot.LaunchFingerprint {
			continue
		}
		if strings.TrimSpace(item.CWD) == strings.TrimSpace(snapshot.CWD) &&
			item.SelectedModelID == snapshot.SelectedModelID {
			continue
		}
		next.Discoveries = append(next.Discoveries, item)
	}
	next.Discoveries = append(next.Discoveries, snapshot)
	next = NormalizeConfiguration(next)
	if err := ValidateConfiguration(next); err != nil {
		return Configuration{}, Agent{}, err
	}
	return next, agent, nil
}
