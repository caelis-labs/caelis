package gatewayapp

import appserver "github.com/caelis-labs/caelis/control/appserver"

type gatewayTestFocusedServices struct {
	appserver.ParticipantService
	appserver.AgentMessageService
	appserver.ConfigurationService
	appserver.AgentService
	appserver.CompletionService
	appserver.PluginService
	appserver.PresentationService
	appserver.TerminalService
}

func gatewayTestAppServerServices(sessions appserver.Service, status appserver.StatusService) appserver.AppServerServices {
	focused := &gatewayTestFocusedServices{}
	return appserver.AppServerServices{
		Sessions: sessions, Participants: focused, AgentMessages: focused, Status: status, Configuration: focused,
		Agents: focused, Completion: focused, Plugins: focused,
		Presentation: focused, Terminal: focused,
	}
}
