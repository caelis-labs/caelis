package controlclient

import (
	"errors"
	"fmt"
)

// AppServerServices is the complete transport-neutral product capability set
// exposed by an AppServer. Presentation surfaces bind a principal once and
// consume AppServerClients; they do not assemble individual Control services.
type AppServerServices struct {
	Sessions      Service
	Participants  ParticipantService
	Status        StatusService
	Configuration ConfigurationService
	Agents        AgentService
	Completion    CompletionService
	Plugins       PluginService
}

// Validate rejects partial AppServer assemblies. A transport adapter may use a
// smaller service directly in focused tests, but a product AppServer is one
// coherent surface boundary.
func (s AppServerServices) Validate() error {
	required := []struct {
		name  string
		value any
	}{
		{name: "Session", value: s.Sessions},
		{name: "participant", value: s.Participants},
		{name: "status", value: s.Status},
		{name: "configuration", value: s.Configuration},
		{name: "Agent", value: s.Agents},
		{name: "completion", value: s.Completion},
		{name: "plugin", value: s.Plugins},
	}
	for _, item := range required {
		if item.value == nil {
			return fmt.Errorf("controlclient: %s AppServer service is required", item.name)
		}
	}
	return nil
}

// AppServerClients is the principal-bound capability set consumed by a
// presentation surface. Embedded and HTTP transports implement these same
// interfaces.
type AppServerClients struct {
	Sessions      SessionClient
	Participants  ParticipantClient
	Status        StatusClient
	Configuration ConfigurationClient
	Agents        AgentClient
	Completion    CompletionClient
	Plugins       PluginClient
}

// BindAppServerClients binds one trusted principal to a complete AppServer.
func BindAppServerClients(services AppServerServices, principal Principal) (AppServerClients, error) {
	if err := services.Validate(); err != nil {
		return AppServerClients{}, err
	}
	sessions, err := BindSessionClient(services.Sessions, principal)
	if err != nil {
		return AppServerClients{}, err
	}
	participants, err := BindParticipantClient(services.Participants, principal)
	if err != nil {
		return AppServerClients{}, err
	}
	status, err := BindStatusClient(services.Status, principal)
	if err != nil {
		return AppServerClients{}, err
	}
	configuration, err := BindConfigurationClient(services.Configuration, principal)
	if err != nil {
		return AppServerClients{}, err
	}
	agents, err := BindAgentClient(services.Agents, principal)
	if err != nil {
		return AppServerClients{}, err
	}
	completion, err := BindCompletionClient(services.Completion, principal)
	if err != nil {
		return AppServerClients{}, err
	}
	plugins, err := BindPluginClient(services.Plugins, principal)
	if err != nil {
		return AppServerClients{}, err
	}
	clients := AppServerClients{
		Sessions: sessions, Participants: participants, Status: status,
		Configuration: configuration, Agents: agents, Completion: completion, Plugins: plugins,
	}
	if err := clients.Validate(); err != nil {
		return AppServerClients{}, err
	}
	return clients, nil
}

// Validate rejects a partial presentation client facade.
func (c AppServerClients) Validate() error {
	if c.Sessions == nil || c.Participants == nil || c.Status == nil || c.Configuration == nil ||
		c.Agents == nil || c.Completion == nil || c.Plugins == nil {
		return errors.New("controlclient: complete AppServer clients are required")
	}
	return nil
}
