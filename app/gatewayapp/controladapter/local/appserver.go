package local

import (
	"errors"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	bridgeassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
)

// AppServer is the embedded product AppServer assembly. It owns the complete
// surface-facing capability set. Surfaces bind clients from this boundary and
// never receive the Host Stack.
type AppServer struct {
	Services appserver.AppServerServices
}

// NewAppServer assembles one complete embedded AppServer from the product Host.
func NewAppServer(host *gatewayapp.Stack) (*AppServer, error) {
	if host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	agentMessages, err := NewAgentMessageService(host)
	if err != nil {
		return nil, err
	}
	status, err := NewStatusService(host)
	if err != nil {
		return nil, err
	}
	configuration, err := NewConfigurationService(host)
	if err != nil {
		return nil, err
	}
	agents, err := NewAgentService(host)
	if err != nil {
		return nil, err
	}
	completion, err := NewCompletionService(host)
	if err != nil {
		return nil, err
	}
	plugins, err := NewPluginService(host)
	if err != nil {
		return nil, err
	}
	dependencies, err := host.PresentationDependencies()
	if err != nil {
		return nil, err
	}
	modes, configs := bridgeassembly.ProvidersFromAssembly(bridgeassembly.ProviderConfig{
		AppName: dependencies.AppName, UserID: dependencies.UserID,
		Assembly: dependencies.Assembly, Sessions: dependencies.Sessions,
	})
	presentation, err := NewPresentationService(host, modes, len(dependencies.Assembly.Modes) > 0, configs)
	if err != nil {
		return nil, err
	}
	terminal, err := NewTerminalService(host.TaskStreams(), host.ControlTerminalStreams())
	if err != nil {
		return nil, err
	}
	server := &AppServer{
		Services: appserver.AppServerServices{
			Sessions: host.ControlClient(), Participants: host.ControlParticipants(), AgentMessages: agentMessages, Status: status,
			Configuration: configuration, Agents: agents, Completion: completion, Plugins: plugins,
			Presentation: presentation, Terminal: terminal, Tasks: host.TaskStreams(),
		},
	}
	if err := server.Services.Validate(); err != nil {
		return nil, err
	}
	return server, nil
}

// Bind returns the principal-bound AppServer clients used by one surface.
func (s *AppServer) Bind(principal appserver.Principal) (appserver.AppServerClients, error) {
	if s == nil {
		return appserver.AppServerClients{}, errors.New("app/gatewayapp/controladapter/local: AppServer is unavailable")
	}
	clients, err := appserver.BindAppServerClients(s.Services, principal)
	if err != nil {
		return appserver.AppServerClients{}, err
	}
	return clients, nil
}
