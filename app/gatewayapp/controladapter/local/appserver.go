package local

import (
	"errors"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	controladapter "github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
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
	sessions := host.Sessions()
	kernelReads := host.ControlKernelReads()
	runtimes := host.ControlRuntimes()
	agentMessageDelivery := host.AgentMessageDelivery()
	workspaceReads := host.WorkspaceReads()
	statusReads := host.ControlStatus()
	agentReads := host.Agents()
	modelReads := host.Models()
	skillReads := host.Skills()
	pluginReads := host.ControlPluginReads()
	preparationReads := host.ACPPreparationReads()
	control := host.ControlClient()
	configurationCommands := host.ConfigurationCommands()
	agentCommands := host.AgentCommands()
	pluginCommands := host.PluginCommands()
	participants := host.ControlParticipants()
	tasks := host.TaskStreams()
	terminalStreams := host.ControlTerminalStreams()
	if sessions == nil || control == nil || configurationCommands == nil || agentCommands == nil ||
		pluginCommands == nil || participants == nil || tasks == nil || terminalStreams == nil {
		return nil, errors.New("app/gatewayapp/controladapter/local: Host AppServer dependencies are unavailable")
	}
	sessionDeps := sessionRuntimeDeps(sessions, host.AppName(), host.UserID(), host.Workspace())
	gatewayDeps := gatewayRuntimeDeps(kernelReads)
	statusDeps := controladapter.StatusAssemblyDeps{
		Gateway: gatewayDeps, Session: sessionDeps, Status: statusRuntimeDeps(statusReads),
		Agent: agentRuntimeDeps(agentReads), Model: modelRuntimeDeps(modelReads), Sandbox: sandboxRuntimeDeps(statusReads),
	}
	agentDeps := controladapter.AgentAssemblyDeps{
		Gateway: gatewayDeps, Session: sessionDeps, Agent: agentRuntimeDeps(agentReads),
	}
	completionDeps := controladapter.CompletionAssemblyDeps{
		Session: sessionDeps, Status: statusRuntimeDeps(statusReads), Agent: agentRuntimeDeps(agentReads),
		Model: modelRuntimeDeps(modelReads), Skill: skillRuntimeDeps(skillReads), Plugin: pluginRuntimeDeps(pluginReads),
	}
	pluginDeps := controladapter.PluginAssemblyDeps{Plugin: pluginRuntimeDeps(pluginReads)}

	agentMessages, err := newAgentMessageService(sessions, agentMessageDelivery.Deliver)
	if err != nil {
		return nil, err
	}
	status, err := newStatusService(statusServiceDeps{
		hostDeps: &statusDeps, acquireRuntime: runtimes.Acquire, resolveWorkspace: workspaceReads.Resolve,
		sandboxStatusForWorkspace: statusReads.SandboxForWorkspace, doctorForWorkspace: statusReads.DoctorForWorkspace,
	})
	if err != nil {
		return nil, err
	}
	configuration, err := newConfigurationService(configurationCommands)
	if err != nil {
		return nil, err
	}
	bindingStatus := host.AgentBindings()
	agents, err := newAgentService(agentServiceDeps{
		hostDeps: &agentDeps, acquireRuntime: runtimes.Acquire, handoff: control.Handoff,
		preparation: preparationReads.Preparation, disconnectCandidates: agentReads.DisconnectCandidatesSnapshot,
		bindingStatus: bindingStatus.AgentBindingStatus, commands: agentCommands,
	})
	if err != nil {
		return nil, err
	}
	completion, err := newCompletionService(completionServiceDeps{
		hostDeps: &completionDeps, acquireRuntime: runtimes.Acquire, resolveWorkspace: workspaceReads.Resolve,
		currentSkillCatalog: workspaceReads.CurrentSkillCatalog, listSessions: control.ListSessions,
	})
	if err != nil {
		return nil, err
	}
	plugins, err := newPluginService(&pluginDeps, pluginCommands)
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
	presentation, err := newPresentationService(
		dependencies.Sessions,
		host.ACPSurface(modes, len(dependencies.Assembly.Modes) > 0, configs),
		agentReads.ControllerStatus,
		len(dependencies.Assembly.Modes) > 0 && modes != nil,
	)
	if err != nil {
		return nil, err
	}
	terminal, err := NewTerminalService(tasks, terminalStreams)
	if err != nil {
		return nil, err
	}
	server := &AppServer{
		Services: appserver.AppServerServices{
			Sessions: control, Participants: participants, AgentMessages: agentMessages, Status: status,
			Configuration: configuration, Agents: agents, Completion: completion, Plugins: plugins,
			Presentation: presentation, Terminal: terminal, Tasks: tasks,
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
