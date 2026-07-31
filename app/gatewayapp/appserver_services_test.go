package gatewayapp

import controlclient "github.com/caelis-labs/caelis/control/client"

type gatewayTestFocusedServices struct {
	controlclient.ParticipantService
	controlclient.ConfigurationService
	controlclient.AgentService
	controlclient.CompletionService
	controlclient.PluginService
}

func gatewayTestAppServerServices(sessions controlclient.Service, status controlclient.StatusService) controlclient.AppServerServices {
	focused := &gatewayTestFocusedServices{}
	return controlclient.AppServerServices{
		Sessions: sessions, Participants: focused, Status: status, Configuration: focused,
		Agents: focused, Completion: focused, Plugins: focused,
	}
}
