package configstore

import (
	"slices"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/agentregistry"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// removeRetiredACPConnection drops the former preset and its live references
// from the detached AppConfig. Custom launchers have their own connection IDs.
func removeRetiredACPConnection(doc *AppConfig) {
	const retired = agentregistry.RetiredAntigravityID
	roster := &doc.ExternalAgents
	roster.Connections = slices.DeleteFunc(roster.Connections, func(connection agents.Connection) bool {
		return connection.ID == retired
	})
	roster.Discoveries = slices.DeleteFunc(roster.Discoveries, func(discovery agents.DiscoverySnapshot) bool {
		return discovery.ConnectionID == retired
	})
	var retiredAgents []string
	roster.Agents = slices.DeleteFunc(roster.Agents, func(agent agents.Agent) bool {
		if agent.ConnectionID != retired {
			return false
		}
		retiredAgents = append(retiredAgents, agent.ID)
		return true
	})
	if len(retiredAgents) == 0 {
		return
	}
	for _, profile := range doc.ModelProfiles.Profiles {
		if profile.Kind() == modelprofile.BackendACP && slices.Contains(retiredAgents, profile.Backend.ACP.AgentID) {
			doc.ModelProfiles = modelprofile.Remove(doc.ModelProfiles, profile.ID)
			doc.AgentBindings, _ = agentbinding.RemoveProfileBindings(doc.AgentBindings, profile.ID)
		}
	}
}
