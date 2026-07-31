package local

import (
	"errors"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	controlclient "github.com/caelis-labs/caelis/control/client"
	bridgeassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

// AppServer is the embedded product AppServer assembly. It owns the complete
// focused Control service set plus the independent Task output side channel.
// Surfaces bind clients from this boundary and never receive the Host Stack.
type AppServer struct {
	Services    controlclient.AppServerServices
	TaskStreams taskstream.Service
}

// NewAppServer assembles one complete embedded AppServer from the product Host.
func NewAppServer(host *gatewayapp.Stack) (*AppServer, error) {
	if host == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: host Stack is required")
	}
	status, err := NewStatusService(host)
	if err != nil {
		return nil, err
	}
	configuration, err := NewConfigurationService(host, status)
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
		Services: controlclient.AppServerServices{
			Sessions: host.ControlClient(), Participants: host.ControlParticipants(), Status: status,
			Configuration: configuration, Agents: agents, Completion: completion, Plugins: plugins,
			Presentation: presentation, Terminal: terminal,
		},
		TaskStreams: host.TaskStreams(),
	}
	if err := server.Services.Validate(); err != nil {
		return nil, err
	}
	if server.TaskStreams == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: Task stream service is required")
	}
	return server, nil
}

// Bind returns the principal-bound AppServer clients used by one surface.
func (s *AppServer) Bind(principal controlclient.Principal) (controlclient.AppServerClients, taskstream.Client, error) {
	if s == nil || s.TaskStreams == nil {
		return controlclient.AppServerClients{}, nil, errors.New("app/gatewayapp/controladapter/local: AppServer is unavailable")
	}
	clients, err := controlclient.BindAppServerClients(s.Services, principal)
	if err != nil {
		return controlclient.AppServerClients{}, nil, err
	}
	tasks, err := taskstream.BindClient(s.TaskStreams, taskstream.Principal{ID: principal.ID, Roles: append([]string(nil), principal.Roles...)})
	if err != nil {
		return controlclient.AppServerClients{}, nil, err
	}
	return clients, tasks, nil
}
